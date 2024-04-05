// Copyright 2024 The Kube-burner Authors.
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

package ocp

import (
	"os"
	"strings"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func CustomWorkload(wh *workloads.WorkloadHelper) *cobra.Command {
	var configFile, benchmarkName string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Runs custom workload",
		PreRun: func(cmd *cobra.Command, args []string) {
			wh.Metadata.Benchmark = benchmarkName
		},
		Run: func(cmd *cobra.Command, args []string) {
			if _, err := os.Stat(configFile); err != nil {
				log.Fatalf("Error reading custom configuration file: %v", err.Error())
			}
			configFileName := strings.Split(configFile, ".")[0]
			wh.Run(configFileName, getMetrics(cmd, "metrics.yml"), alertsProfiles)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Config file path or URL")
	cmd.Flags().StringVarP(&benchmarkName, "benchmark", "b", "custom-workload", "Name of the benchmark")
	cmd.MarkFlagRequired("config")
	return cmd
}
