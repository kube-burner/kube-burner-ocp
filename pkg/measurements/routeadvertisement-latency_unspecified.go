//go:build !linux
// +build !linux

package measurements

import (
	"fmt"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	"github.com/kube-burner/kube-burner/pkg/measurements/types"
)

func NewRaLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any) (measurements.MeasurementFactory, error) {
	return nil, fmt.Errorf("raLatencyMeasurement is supported only when running on Linux")
}
