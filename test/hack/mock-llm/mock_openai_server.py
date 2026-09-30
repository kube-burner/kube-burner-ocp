#!/usr/bin/env python3
# Copyright 2026 The Kube-burner Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Scriptable OpenAI-compatible mock LLM for agentic OpenShift Lightspeed load tests.

Why this exists
---------------
lightspeed-service ships a `fake_provider`, but the agentic stack cannot reach
it: `LLMProvider.spec.type` is a CEL-enforced enum of five real providers
(Anthropic, GoogleCloudVertex, OpenAI, AzureOpenAI, AWSBedrock). Presenting an
OpenAI-shaped endpoint and declaring `type: OpenAI` is the only supported way to
put a deterministic model behind an agentic sandbox.

A fixed-response mock is not enough either. An agentic sandbox runs a *loop*:
the model emits tool calls, the sandbox executes them and feeds the results
back, up to `Agent.spec.maxTurns`. A mock that always answers terminally
collapses every run to one turn and measures nothing. So this server is
scriptable -- a named profile decides how many tool-call turns to emit before
the final structured answer.

Stdlib only, on purpose: it is delivered as a ConfigMap and run by a stock UBI
python image, so there is no image to build and no pip install to fail on a
disconnected cluster.

How a turn is chosen
--------------------
The server is stateless. The agent framework resends the whole conversation on
every call, so the turn index is simply the number of assistant messages
already present. Turn N < profile.tool_calls emits a tool call; the turn that
reaches the budget emits the terminal structured payload.

How a step is chosen
--------------------
Each workflow step asks for a different JSON schema, so the step is recovered
by looking for schema-specific markers in the request body. `[mock-step:...]`
in the prompt overrides the guess.

Prompt tokens
-------------
The sandbox controls its own HTTP headers, so the only channel the load
generator owns is the request text. Two tokens are recognised anywhere in the
conversation:

    [mock-profile:typical]      select a response profile
    [mock-ns:perf-target-0]     emit RBAC scoped to this namespace, so the
                                operator actually materializes a Role and
                                RoleBinding at execution

`X-Mock-Profile` is honoured too, for curl-driven smoke tests.
"""

import json
import os
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN_PORT = int(os.environ.get("PORT", "8080"))
DEFAULT_PROFILE = os.environ.get("DEFAULT_PROFILE", "typical")
EXTRA_SLEEP = float(os.environ.get("MOCK_SLEEP_SECONDS", "0"))
MODEL_NAME = os.environ.get("MOCK_MODEL", "mock-model")
LOG_REQUESTS = os.environ.get("MOCK_LOG_REQUESTS", "") not in ("", "0", "false")

PROFILE_TOKEN = re.compile(r"\[mock-profile:([a-zA-Z0-9_-]+)\]")
NAMESPACE_TOKEN = re.compile(r"\[mock-ns:([a-z0-9][-a-z0-9]{0,61}[a-z0-9])\]")
STEP_TOKEN = re.compile(r"\[mock-step:(analysis|execution|verification|escalation)\]")
ROLE_SENTENCE = re.compile(
    r"You are an?\s+(analysis|execution|verification|escalation)\s+agent", re.IGNORECASE
)

PROFILES = {
    "trivial": {"tool_calls": 0, "options": 0, "actions": 0, "sleep": 0.0},
    "short": {"tool_calls": 4, "options": 1, "actions": 3, "sleep": 0.0},
    "typical": {"tool_calls": 12, "options": 2, "actions": 6, "sleep": 0.0},
    "long": {"tool_calls": 55, "options": 3, "actions": 12, "sleep": 0.0},
    "max-turns": {"tool_calls": 10**9, "options": 1, "actions": 1, "sleep": 0.0},
    "timeout": {"tool_calls": 10**9, "options": 1, "actions": 1, "sleep": 30.0},
    "malformed": {"tool_calls": 2, "options": 1, "actions": 1, "sleep": 0.0},
}

_profiles_path = os.environ.get("MOCK_PROFILES_PATH", "")
if _profiles_path and os.path.exists(_profiles_path):
    with open(_profiles_path, encoding="utf-8") as fh:
        for _name, _spec in json.load(fh).items():
            PROFILES[_name] = {**PROFILES.get(_name, PROFILES["typical"]), **_spec}


class Metrics:
    """Prometheus text exposition, hand rolled to avoid a client dependency.

    These counters are the point of instrumenting the mock at all: turn and
    token KPIs become scrapeable by the same kube-burner metrics profile that
    collects everything else, instead of needing a span reader to recover them
    from OTLP.
    """

    BUCKETS = (0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120)

    def __init__(self):
        self._lock = threading.Lock()
        self.requests = {}
        self.turns = {}
        self.tokens = {"input": 0, "output": 0}
        self.duration_buckets = dict.fromkeys(self.BUCKETS, 0)
        self.duration_count = 0
        self.duration_sum = 0.0
        self.inflight = 0

    def observe(self, profile, step, terminal, prompt_tokens, completion_tokens, seconds):
        key = (profile, step)
        with self._lock:
            self.requests[key] = self.requests.get(key, 0) + 1
            if terminal:
                self.turns[key] = self.turns.get(key, 0) + 1
            self.tokens["input"] += prompt_tokens
            self.tokens["output"] += completion_tokens
            self.duration_count += 1
            self.duration_sum += seconds
            for bucket in self.BUCKETS:
                if seconds <= bucket:
                    self.duration_buckets[bucket] += 1

    def track_inflight(self, delta):
        with self._lock:
            self.inflight += delta

    def render(self):
        out = [
            "# HELP mockllm_requests_total Chat completion requests served.",
            "# TYPE mockllm_requests_total counter",
        ]
        with self._lock:
            for (profile, step), count in sorted(self.requests.items()):
                out.append(f'mockllm_requests_total{{profile="{profile}",step="{step}"}} {count}')
            out += [
                "# HELP mockllm_turns_total Agent loops that reached a terminal answer.",
                "# TYPE mockllm_turns_total counter",
            ]
            for (profile, step), count in sorted(self.turns.items()):
                out.append(f'mockllm_turns_total{{profile="{profile}",step="{step}"}} {count}')
            out += [
                f'mockllm_tokens_total{{direction="input"}} {self.tokens["input"]}',
                f'mockllm_tokens_total{{direction="output"}} {self.tokens["output"]}',
                f"mockllm_inflight_requests {self.inflight}",
            ]
            for bucket in self.BUCKETS:
                out.append(
                    f'mockllm_request_duration_seconds_bucket{{le="{bucket}"}} '
                    f"{self.duration_buckets[bucket]}"
                )
            out.append(
                f'mockllm_request_duration_seconds_bucket{{le="+Inf"}} {self.duration_count}'
            )
            out.append(f"mockllm_request_duration_seconds_sum {self.duration_sum:.6f}")
            out.append(f"mockllm_request_duration_seconds_count {self.duration_count}")
        return "\n".join(out) + "\n"


METRICS = Metrics()


def estimate_tokens(text):
    """Roughly four characters per token, enough to make usage non-trivial."""
    return max(1, len(text) // 4)


def detect_step(body_text, prompt_text):
    """Recover which workflow step is being served.

    Returns (step, rule) so the caller can log which evidence decided it.

    The role sentence is authoritative. Everything else is a fallback for a
    prompt this mock has not seen, and the fallbacks are the dangerous part:
    they key on output-schema property names, but a step's request also quotes
    the *previous* step's output. The verification prompt embeds the approved
    remediation, so 'actionsTaken' appears in a request that is emphatically
    not an execution request.

    That is not hypothetical. Keying on 'actionsTaken' first sent every
    verification request an execution-shaped answer, which has no `checks`
    field, so the operator rejected the VerificationResult CR with "result CR
    missing checks" and the run ended Verified=False/SandboxFailed. Nothing in
    the run pointed at the mock; it read as a product defect. The tell was
    mockllm_requests_total carrying no step="verification" series at all.
    [cluster-verified 2026-09-30]

    Order the fallbacks so the step's own distinctive output field wins over a
    field it merely quotes: verification before execution before analysis.
    """
    haystack = body_text + "\n" + prompt_text
    explicit = STEP_TOKEN.search(haystack)
    if explicit:
        return explicit.group(1), "token"
    role = ROLE_SENTENCE.search(prompt_text)
    if role:
        return role.group(1).lower(), "role"
    if "escalation agent" in haystack or "escalation analysis" in haystack:
        return "escalation", "schema"
    if '"checks"' in haystack:
        return "verification", "schema"
    if "actionsTaken" in haystack:
        return "execution", "schema"
    if "actionRequired" in haystack:
        return "analysis", "schema"
    print("mock-llm: step detection found no evidence, defaulting to analysis", flush=True)
    return "analysis", "default"


def conversation_text(messages):
    parts = []
    for message in messages:
        content = message.get("content")
        if isinstance(content, str):
            parts.append(content)
        elif isinstance(content, list):
            for block in content:
                if isinstance(block, dict) and isinstance(block.get("text"), str):
                    parts.append(block["text"])
    return "\n".join(parts)


def select_profile(headers, text):
    token = PROFILE_TOKEN.search(text)
    if token and token.group(1) in PROFILES:
        return token.group(1)
    header = headers.get("X-Mock-Profile", "")
    if header in PROFILES:
        return header
    return DEFAULT_PROFILE if DEFAULT_PROFILE in PROFILES else "typical"


def rbac_block(namespace):
    """RBAC scoped to a namespace the run actually targets.

    Left out unless a namespace is known: `rbac` is optional in the analysis
    schema, and a rule naming a namespace that is not in spec.targetNamespaces
    is rejected. Emitting it when we do know the namespace is what makes the
    operator materialize a Role and RoleBinding at execution, which is a real
    cost centre worth measuring rather than skipping.
    """
    if not namespace:
        return None
    return {
        "namespaceScoped": [
            {
                "namespace": namespace,
                "apiGroups": ["apps"],
                "resources": ["deployments"],
                "resourceNames": ["mock-workload"],
                "verbs": ["patch"],
                "justification": "kubectl patch deployment/mock-workload to apply the mock remediation",
            }
        ]
    }


def analysis_payload(spec, namespace):
    if spec["options"] == 0:
        # actionRequired is a string enum of "True"/"False" in the schema, not
        # a JSON boolean. A real boolean fails structured output validation.
        return {
            "actionRequired": "False",
            "diagnosis": {
                "summary": "Synthetic load-test condition; the reported symptom has already cleared.",
                "rootCause": "No remediation required (mock trivial profile)",
            },
            "options": [],
        }
    options = []
    for index in range(spec["options"]):
        actions = [
            {
                "command": f"kubectl patch deployment/mock-workload -p '{{\"spec\":{{\"replicas\":{step + 1}}}}}'",
                "type": "mutation" if step % 2 == 0 else "post-check",
                "description": f"Mock remediation action {step + 1} for option {index + 1}.",
            }
            for step in range(spec["actions"])
        ]
        option = {
            "title": f"Mock remediation option {index + 1}",
            "summary": "Synthetic remediation produced by the mock LLM for load testing.",
            "diagnosis": {
                "summary": "Synthetic diagnosis generated by the mock LLM. No real cluster condition was inspected.",
                "rootCause": f"Mock root cause {index + 1}",
            },
            "remediationPlan": {
                "description": "Apply the mock actions in order.",
                "actions": actions,
                "reversible": "Reversible",
                "rollbackPlan": {
                    "description": "Revert the mock workload to its previous replica count.",
                    "command": "kubectl rollout undo deployment/mock-workload",
                },
            },
            "verification": {
                "description": "Confirm the mock workload reports the expected replica count.",
                "steps": [
                    {
                        "name": f"mock-check-{step + 1}",
                        "command": "kubectl get deployment/mock-workload -o jsonpath={.status.readyReplicas}",
                        "expected": str(step + 1),
                        "type": "command",
                    }
                    for step in range(max(1, spec["actions"] // 2))
                ],
            },
        }
        rbac = rbac_block(namespace)
        if rbac:
            option["rbac"] = rbac
        options.append(option)
    return {"actionRequired": "True", "options": options}


def execution_payload(spec):
    return {
        "success": True,
        "actionsTaken": [
            {
                "type": "patch",
                "description": f"Applied mock remediation action {index + 1}.",
                "outcome": "Succeeded",
                "output": "deployment.apps/mock-workload patched",
            }
            for index in range(max(1, spec["actions"]))
        ],
    }


def verification_payload(spec):
    return {
        "success": True,
        "checks": [
            {
                "name": f"mock-check-{index + 1}",
                "source": "kubectl get deployment/mock-workload -o jsonpath={.status.readyReplicas}",
                "value": str(index + 1),
                "result": "Passed",
            }
            for index in range(max(1, spec["actions"] // 2))
        ],
        "summary": "All mock verification checks passed.",
    }


def escalation_payload():
    return {
        "success": True,
        "summary": "Mock escalation report.",
        "content": "Verification failed under synthetic load. This report is generated by the mock LLM.",
    }


def terminal_content(step, spec, namespace):
    if step == "execution":
        payload = execution_payload(spec)
    elif step == "verification":
        payload = verification_payload(spec)
    elif step == "escalation":
        payload = escalation_payload()
    else:
        payload = analysis_payload(spec, namespace)
    return json.dumps(payload)


def pick_tool(request_tools):
    """Choose a tool the caller actually advertised.

    Emitting a tool_call for a function the client never declared makes the
    agent framework error out rather than loop, so with no tools on offer the
    only correct move is to answer terminally.
    """
    for tool in request_tools or []:
        function = tool.get("function") or {}
        name = function.get("name")
        if name:
            return name, function.get("parameters") or {}
    return None, None


def tool_arguments(parameters):
    """Minimal arguments that satisfy the tool's required properties."""
    args = {}
    properties = (parameters or {}).get("properties") or {}
    for name in (parameters or {}).get("required") or []:
        prop = properties.get(name) or {}
        kind = prop.get("type", "string")
        if kind == "integer" or kind == "number":
            args[name] = 1
        elif kind == "boolean":
            args[name] = True
        elif kind == "array":
            args[name] = []
        elif kind == "object":
            args[name] = {}
        else:
            args[name] = prop.get("enum", ["mock"])[0]
    return json.dumps(args)


def build_message(turn, spec, step, namespace, request_tools):
    """Return (message, finish_reason, terminal)."""
    tool_name, parameters = pick_tool(request_tools)
    if turn < spec["tool_calls"] and tool_name:
        return (
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": f"call_mock_{turn}",
                        "type": "function",
                        "function": {"name": tool_name, "arguments": tool_arguments(parameters)},
                    }
                ],
            },
            "tool_calls",
            False,
        )
    content = terminal_content(step, spec, namespace)
    if spec is PROFILES.get("malformed") or spec.get("malformed"):
        content = content[: len(content) // 2]
    return {"role": "assistant", "content": content}, "stop", True


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    # Default logging is one line per request at multi-hundred RPS, which turns
    # the mock into the slowest thing in the test.
    def log_message(self, fmt, *args):
        if os.environ.get("MOCK_ACCESS_LOG") == "true":
            sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def _send(self, code, body, content_type="application/json"):
        raw = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path.startswith("/metrics"):
            self._send(200, METRICS.render(), "text/plain; version=0.0.4")
        elif self.path.startswith("/healthz") or self.path.startswith("/readyz"):
            self._send(200, json.dumps({"status": "ok"}))
        elif self.path.rstrip("/").endswith("/models"):
            self._send(
                200,
                json.dumps(
                    {
                        "object": "list",
                        "data": [
                            {"id": MODEL_NAME, "object": "model", "owned_by": "kube-burner-ocp"}
                        ],
                    }
                ),
            )
        else:
            self._send(404, json.dumps({"error": {"message": "not found"}}))

    def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
        if not self.path.rstrip("/").endswith("/chat/completions"):
            self._send(404, json.dumps({"error": {"message": "not found"}}))
            return

        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        body_text = raw.decode("utf-8", errors="replace")
        try:
            request = json.loads(body_text or "{}")
        except json.JSONDecodeError as exc:
            self._send(400, json.dumps({"error": {"message": f"invalid JSON: {exc}"}}))
            return

        started = time.time()
        METRICS.track_inflight(1)
        try:
            self._serve_completion(request, body_text, started)
        finally:
            METRICS.track_inflight(-1)

    def _serve_completion(self, request, body_text, started):
        messages = request.get("messages") or []
        text = conversation_text(messages)
        profile_name = select_profile(self.headers, text)
        spec = dict(PROFILES.get(profile_name, PROFILES["typical"]))
        spec["malformed"] = profile_name == "malformed"
        step, rule = detect_step(body_text, text)
        if LOG_REQUESTS:
            instruction = ROLE_SENTENCE.search(text)
            print(
                f"step={step} rule={rule} profile={profile_name} "
                f"messages={len(messages)} "
                f"role_sentence={instruction.group(0) if instruction else '-'}",
                flush=True,
            )
        namespace_token = NAMESPACE_TOKEN.search(text)
        namespace = namespace_token.group(1) if namespace_token else ""
        turn = sum(1 for message in messages if message.get("role") == "assistant")

        delay = spec.get("sleep", 0.0) + EXTRA_SLEEP
        if delay > 0:
            time.sleep(delay)

        message, finish_reason, terminal = build_message(
            turn, spec, step, namespace, request.get("tools")
        )
        prompt_tokens = estimate_tokens(text)
        completion_tokens = estimate_tokens(
            message.get("content") or json.dumps(message.get("tool_calls") or [])
        )

        if request.get("stream"):
            self._stream(request, message, finish_reason, prompt_tokens, completion_tokens)
        else:
            self._send(
                200,
                json.dumps(
                    {
                        "id": f"chatcmpl-mock-{int(started * 1000)}",
                        "object": "chat.completion",
                        "created": int(started),
                        "model": request.get("model") or MODEL_NAME,
                        "choices": [
                            {"index": 0, "message": message, "finish_reason": finish_reason}
                        ],
                        "usage": {
                            "prompt_tokens": prompt_tokens,
                            "completion_tokens": completion_tokens,
                            "total_tokens": prompt_tokens + completion_tokens,
                        },
                    }
                ),
            )

        METRICS.observe(
            profile_name, step, terminal, prompt_tokens, completion_tokens, time.time() - started
        )

    def _stream(self, request, message, finish_reason, prompt_tokens, completion_tokens):
        """Server-sent events, so the sandbox's streaming path is exercised too.

        Streaming changes how the sandbox handles tokens and must not be
        assumed equivalent to the buffered path.
        """
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

        created = int(time.time())
        model = request.get("model") or MODEL_NAME
        chunk_count = max(1, int(os.environ.get("MOCK_CHUNKS", "8")))

        def emit(delta, reason=None):
            payload = {
                "id": f"chatcmpl-mock-{created}",
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [{"index": 0, "delta": delta, "finish_reason": reason}],
            }
            body = f"data: {json.dumps(payload)}\n\n".encode("utf-8")
            self.wfile.write(f"{len(body):X}\r\n".encode("ascii") + body + b"\r\n")

        emit({"role": "assistant"})
        if message.get("tool_calls"):
            # Tool calls go out whole: splitting a function-argument string
            # across deltas buys nothing and is easy to reassemble wrongly.
            #
            # A streaming tool call delta is NOT the buffered shape. Every entry
            # must carry an integer "index", which is what the client keys on to
            # reassemble a call spread over several chunks. Emitting the
            # buffered shape here fails client-side validation
            # (ChoiceDeltaToolCall.index) before the sandbox sees any answer,
            # and the run dies with SandboxFailed rather than a model error.
            emit({
                "tool_calls": [
                    {**call, "index": position}
                    for position, call in enumerate(message["tool_calls"])
                ]
            })
        else:
            content = message.get("content") or ""
            size = max(1, -(-len(content) // chunk_count))
            for offset in range(0, len(content), size):
                emit({"content": content[offset : offset + size]})
        emit({}, finish_reason)

        usage = {
            "id": f"chatcmpl-mock-{created}",
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [],
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "total_tokens": prompt_tokens + completion_tokens,
            },
        }
        body = f"data: {json.dumps(usage)}\n\n".encode("utf-8")
        self.wfile.write(f"{len(body):X}\r\n".encode("ascii") + body + b"\r\n")
        done = b"data: [DONE]\n\n"
        self.wfile.write(f"{len(done):X}\r\n".encode("ascii") + done + b"\r\n")
        self.wfile.write(b"0\r\n\r\n")


def main():
    server = ThreadingHTTPServer(("", LISTEN_PORT), Handler)
    server.daemon_threads = True
    sys.stderr.write(
        f"mock-llm listening on :{LISTEN_PORT} default-profile={DEFAULT_PROFILE} "
        f"profiles={','.join(sorted(PROFILES))}\n"
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
