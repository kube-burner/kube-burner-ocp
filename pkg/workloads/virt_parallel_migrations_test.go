// Copyright 2026 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workloads

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func migrationGuest(name, node, uid string, state map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "uid": uid},
		"status":   map[string]any{"phase": "Running", "nodeName": node, "migrationState": state},
	}
}

const (
	migrationAllGuests          = "all guests on different nodes"
	migrationOmittedFailed      = "omitted failed field"
	migrationPartialCompletion  = "partial completion"
	migrationStale              = "stale migration"
	migrationFailed             = "failed migration"
	migrationSameNode           = "same node"
	migrationReplacement        = "replaced VMI"
	migrationMissingBaseline    = "missing baseline"
	migrationAPIFailure         = "API failure"
	migrationEmpty              = "empty"
	migrationPartialPopulation  = "partial population"
	migrationMismatchedSelector = "mismatched VM selector"
)

func TestVirtParallelMigrationHooks(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("migration hooks require jq")
	}
	script, err := filepath.Abs("../../cmd/config/scripts/check_migrations.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{migrationAllGuests, migrationOmittedFailed, migrationEmpty, migrationPartialPopulation, migrationPartialCompletion, migrationStale, migrationFailed, migrationSameNode, migrationReplacement, migrationMissingBaseline, migrationMismatchedSelector, migrationAPIFailure} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mock := "#!/bin/bash\nif [[ ${MOCK_API_ERROR:-} == true ]]; then exit 1; fi\nif [[ $2 == vm ]]; then cat \"$MOCK_VMS\"; else cat \"$MOCK_VMIS\"; fi\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(mock), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MOCK_VMS", filepath.Join(dir, "vms.json"))
			t.Setenv("MOCK_VMIS", filepath.Join(dir, "vmis.json"))
			write := func(file string, items []map[string]any) {
				t.Helper()
				b, err := json.Marshal(map[string]any{"items": items})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, file), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := []map[string]any{migrationGuest("vm-1", "node-a", "uid-1", nil), migrationGuest("vm-2", "node-b", "uid-2", nil)}
			if name == migrationStale {
				before[0]["status"].(map[string]any)["migrationState"] = map[string]any{"migrationUid": "migration-1"}
			}
			if name == migrationEmpty {
				before = []map[string]any{}
			}
			if name == migrationPartialPopulation {
				before = before[:1]
			}
			write("vmis.json", before)
			write("vms.json", before)
			if name == migrationMismatchedSelector {
				write("vms.json", before[:1])
			}
			if name == migrationAPIFailure {
				t.Setenv("MOCK_API_ERROR", "true")
			}
			baseline := filepath.Join(dir, "before.json")
			run := func(action string) (string, error) {
				t.Helper()
				cmd := exec.Command("bash", script, action, "test-ns", "test=iteration-0", "2", baseline)
				b, err := cmd.CombinedOutput()
				return string(b), err
			}
			output, err := run("prepare")
			if name == migrationEmpty || name == migrationPartialPopulation || name == migrationMismatchedSelector || name == migrationAPIFailure {
				if err == nil {
					t.Fatalf("empty or incorrect selection passed: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare: %v %s", err, output)
			}
			success := func(uid, source, target string) map[string]any {
				return map[string]any{"migrationUid": uid, "sourceNode": source, "targetNode": target, "completed": true, "failed": false}
			}
			first := success("migration-1", "node-a", "node-c")
			second := success("migration-2", "node-b", "node-a")
			after := []map[string]any{migrationGuest("vm-1", "node-c", "uid-1", first), migrationGuest("vm-2", "node-a", "uid-2", second)}
			switch name {
			case migrationOmittedFailed:
				delete(first, "failed")
				delete(second, "failed")
			case migrationPartialCompletion:
				second["completed"] = false
			case migrationFailed:
				first["failed"] = true
			case migrationSameNode:
				first["targetNode"] = "node-a"
				after[0]["status"].(map[string]any)["nodeName"] = "node-a"
			case migrationReplacement:
				after[0]["metadata"].(map[string]any)["uid"] = "replacement"
			case migrationMissingBaseline:
				baseline = filepath.Join(dir, "absent.json")
			}
			write("vmis.json", after)
			output, err = run("verify")
			if name == migrationAllGuests || name == migrationOmittedFailed {
				if err != nil || !strings.Contains(output, "completed=2") {
					t.Fatalf("verify: %v %s", err, output)
				}
			} else if err == nil {
				t.Fatalf("incomplete migration passed: %s", output)
			}
		})
	}
}
