// Copyright 2017 the Istio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aspect

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	rpc "github.com/googleapis/googleapis/google/rpc"
	multierror "github.com/hashicorp/go-multierror"

	dpb "istio.io/api/mixer/v1/config/descriptor"
	"istio.io/mixer/pkg/adapter"
	aconfig "istio.io/mixer/pkg/aspect/config"
	"istio.io/mixer/pkg/attribute"
	"istio.io/mixer/pkg/config"
	"istio.io/mixer/pkg/config/descriptor"
	cpb "istio.io/mixer/pkg/config/proto"
	"istio.io/mixer/pkg/expr"
	"istio.io/mixer/pkg/status"
)

type (
	metricsManager struct{}

	metricInfo struct {
		definition *adapter.MetricDefinition
		value      string
		labels     map[string]string
	}

	metricsExecutor struct {
		name     string
		aspect   adapter.MetricsAspect
		metadata map[string]*metricInfo // metric name -> info
	}
)

// newMetricsManager returns a manager for the metric aspect.
func newMetricsManager() ReportManager {
	return &metricsManager{}
}

func (m *metricsManager) NewReportExecutor(c *cpb.Combined, a adapter.Builder, env adapter.Env, df descriptor.Finder) (ReportExecutor, error) {
	params := c.Aspect.Params.(*aconfig.MetricsParams)

	metadata := make(map[string]*metricInfo)
	defs := make(map[string]*adapter.MetricDefinition, len(params.Metrics))
	for _, metric := range params.Metrics {
		// we ignore the error as config validation confirms both that the metric exists and that it can
		// be converted safely into its definition
		def, _ := metricDefinitionFromProto(df.GetMetric(metric.DescriptorName))
		defs[def.Name] = def
		metadata[def.Name] = &metricInfo{
			definition: def,
			value:      metric.Value,
			labels:     metric.Labels,
		}
	}
	b := a.(adapter.MetricsBuilder)
	asp, err := b.NewMetricsAspect(env, c.Builder.Params.(adapter.Config), defs)
	if err != nil {
		return nil, fmt.Errorf("failed to construct metrics aspect with config '%v' and err: %s", c, err)
	}
	return &metricsExecutor{b.Name(), asp, metadata}, nil
}

func (*metricsManager) Kind() config.Kind                  { return config.MetricsKind }
func (*metricsManager) DefaultConfig() config.AspectParams { return &aconfig.MetricsParams{} }

func (*metricsManager) ValidateConfig(c config.AspectParams, v expr.Validator, df descriptor.Finder) (ce *adapter.ConfigErrors) {
	cfg := c.(*aconfig.MetricsParams)
	for _, metric := range cfg.Metrics {
		desc := df.GetMetric(metric.DescriptorName)
		if desc == nil {
			ce = ce.Appendf("Metrics", "could not find a descriptor for the metric '%s'", metric.DescriptorName)
			continue // we can't do any other validation without the descriptor
		}

		if err := v.AssertType(metric.Value, df, desc.Value); err != nil {
			ce = ce.Appendf(fmt.Sprintf("Metric[%s].Value", metric.DescriptorName), "error type checking label %s: %v", err)
		}
		ce = ce.Extend(validateLabels(fmt.Sprintf("Metrics[%s].Labels", desc.Name), metric.Labels, desc.Labels, v, df))

		// TODO: this doesn't feel like quite the right spot to do this check, but it's the best we have ¯\_(ツ)_/¯
		if _, err := metricDefinitionFromProto(desc); err != nil {
			ce = ce.Appendf(fmt.Sprintf("Descriptor[%s]", desc.Name), "failed to marshal descriptor into its adapter representation with err: %v", err)
		}
	}
	return
}

func (w *metricsExecutor) Execute(attrs attribute.Bag, mapper expr.Evaluator) rpc.Status {
	result := &multierror.Error{}
	var values []adapter.Value

	for name, md := range w.metadata {
		metricValue, err := mapper.Eval(md.value, attrs)
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to eval metric value for metric '%s' with err: %s", name, err))
			continue
		}
		labels, err := evalAll(md.labels, attrs, mapper)
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("failed to eval labels for metric '%s' with err: %s", name, err))
			continue
		}

		// TODO: investigate either pooling these, or keeping a set around that has only its field's values updated.
		// we could keep a map[metric name]value, iterate over the it updating only the fields in each value
		values = append(values, adapter.Value{
			Definition: md.definition,
			Labels:     labels,
			// TODO: extract standard timestamp attributes for start/end once we det'm what they are
			StartTime:   time.Now(),
			EndTime:     time.Now(),
			MetricValue: metricValue,
		})
	}

	if err := w.aspect.Record(values); err != nil {
		result = multierror.Append(result, fmt.Errorf("failed to record all values with err: %s", err))
	}

	if glog.V(4) {
		glog.V(4).Infof("completed execution of metric adapter '%s' for %d values", w.name, len(values))
	}

	err := result.ErrorOrNil()
	if err != nil {
		return status.WithError(err)
	}

	return status.OK
}

func (w *metricsExecutor) Close() error {
	return w.aspect.Close()
}

func metricDefinitionFromProto(desc *dpb.MetricDescriptor) (*adapter.MetricDefinition, error) {
	labels := make(map[string]adapter.LabelType, len(desc.Labels))
	for _, label := range desc.Labels {
		l, err := valueTypeToLabelType(label.ValueType)
		if err != nil {
			return nil, fmt.Errorf("descriptor '%s' label '%s' failed to convert label type value '%v' from proto with err: %s",
				desc.Name, label.Name, label.ValueType, err)
		}
		labels[label.Name] = l
	}
	kind, err := metricKindFromProto(desc.Kind)
	if err != nil {
		return nil, fmt.Errorf("descriptor '%s' failed to convert metric kind value '%v' from proto with err: %s",
			desc.Name, desc.Kind, err)
	}
	return &adapter.MetricDefinition{
		Name:        desc.Name,
		DisplayName: desc.DisplayName,
		Description: desc.Description,
		Kind:        kind,
		Labels:      labels,
	}, nil
}
