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
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
)

func TestVirtParallelMigrationConfiguration(t *testing.T) {
	b, err := os.ReadFile("../../cmd/config/virt-parallel/virt-parallel.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, skip := range []bool{false, true} {
		vars := map[string]any{"GC": false, "GC_METRICS": false, "counter": 1, "testNamespace": "virt-parallel", "vmCount": 20,
			"rootVolumeSize": 6, "dataVolumeSize": 1, "volumeSizeIncrement": 1,
			"skipMigrationJob": skip, "skipResizeJob": false, "skipRestartJob": false, "skipSnapshotJob": false,
			"useBaseImage": false, "dataVolumeCounters": []int{1}, "NODE_SELECTOR": "{}"}
		spec, err := config.ParseWithUserdata("test-uuid", time.Hour, bytes.NewReader(b), nil, true, vars, nil)
		if err != nil {
			t.Fatalf("skip=%v: %v", skip, err)
		}
		found := false
		for _, job := range spec.Jobs {
			if job.Name != "virt-parallel-migrate-vms-1" {
				continue
			}
			found = true
			if !job.WaitWhenFinished || len(job.Objects) != 1 {
				t.Fatalf("migration must wait for all VMs: %+v", job)
			}
			selector := job.Objects[0].LabelSelector
			if len(selector) != 3 || selector["virt-parallel.kube-burner.io/counter"] != "counter-1" ||
				selector["virt-parallel.kube-burner.io/namespace"] != "virt-parallel" || selector["kube-burner.io/uuid"] != "test-uuid" {
				t.Fatalf("migration selected a node or another iteration: %v", selector)
			}
			prepared, verified := false, false
			for _, hook := range job.Hooks {
				if hook.Cmd[0] != "./check_migrations.sh" {
					continue
				}
				if hook.Cmd[4] != strconv.Itoa(20) {
					t.Fatalf("wrong migration count: %v", hook.Cmd)
				}
				if hook.Cmd[1] == "prepare" && hook.When == "beforeJobExecution" {
					prepared = true
				}
				if hook.Cmd[1] == "verify" && hook.When == "beforeCleanup" {
					verified = true
				}
			}
			if !prepared || !verified {
				t.Fatalf("migration evidence hooks missing or misplaced: %+v", job.Hooks)
			}
		}
		if found == skip {
			t.Fatalf("migration inclusion does not match skip flag: skip=%v found=%v", skip, found)
		}
	}
}
