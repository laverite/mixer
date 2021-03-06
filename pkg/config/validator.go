// Copyright 2017 Istio Authors
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

// Package config handles configuration ingestion and processing.
// validator
// 1. Accepts new configuration from user
// 2. Validates configuration
// 3. Produces a "ValidatedConfig"
// runtime
// 1. It is validated and actionable configuration
// 2. It resolves the configuration to a list of Combined {aspect, adapter} configs
//    given an attribute.Bag.
// 3. Combined config has complete information needed to dispatch aspect
package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ghodss/yaml"
	"github.com/gogo/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"

	"istio.io/mixer/pkg/adapter"
	"istio.io/mixer/pkg/config/descriptor"
	pb "istio.io/mixer/pkg/config/proto"
	"istio.io/mixer/pkg/expr"
)

type (
	// AspectParams describes configuration parameters for an aspect.
	AspectParams proto.Message

	// AspectValidator describes a type that is able to validate Aspect configuration.
	AspectValidator interface {
		// DefaultConfig returns a default configuration struct for this
		// adapter. This will be used by the configuration system to establish
		// the shape of the block of configuration state passed to the NewAspect method.
		DefaultConfig() (c AspectParams)

		// ValidateConfig determines whether the given configuration meets all correctness requirements.
		ValidateConfig(c AspectParams, validator expr.Validator, finder descriptor.Finder) *adapter.ConfigErrors
	}

	// BuilderValidatorFinder is used to find specific underlying validators.
	// Manager registry and adapter registry should implement this interface
	// so ConfigValidators can be uniformly accessed.
	BuilderValidatorFinder func(name string) (adapter.ConfigValidator, bool)

	// AspectValidatorFinder is used to find specific underlying validators.
	// Manager registry and adapter registry should implement this interface
	// so ConfigValidators can be uniformly accessed.
	AspectValidatorFinder func(kind Kind) (AspectValidator, bool)

	// AdapterToAspectMapper returns the set of aspect kinds implemented by
	// the given builder.
	AdapterToAspectMapper func(builder string) KindSet
)

// newValidator returns a validator given component validators.
func newValidator(managerFinder AspectValidatorFinder, adapterFinder BuilderValidatorFinder,
	findAspects AdapterToAspectMapper, strict bool, exprValidator expr.Validator) *validator {
	return &validator{
		managerFinder: managerFinder,
		adapterFinder: adapterFinder,
		findAspects:   findAspects,
		strict:        strict,
		exprValidator: exprValidator,
		validated:     &Validated{},
	}
}

type (
	// validator is the Configuration validator.
	validator struct {
		managerFinder    AspectValidatorFinder
		adapterFinder    BuilderValidatorFinder
		findAspects      AdapterToAspectMapper
		descriptorFinder descriptor.Finder
		strict           bool
		exprValidator    expr.Validator
		validated        *Validated
	}

	adapterKey struct {
		kind Kind
		name string
	}
	// Validated store validated configuration.
	// It has been validated as internally consistent and correct.
	Validated struct {
		adapterByName map[adapterKey]*pb.Adapter
		globalConfig  *pb.GlobalConfig
		serviceConfig *pb.ServiceConfig
		numAspects    int
	}
)

func (a adapterKey) String() string {
	return fmt.Sprintf("%s//%s", a.kind, a.name)
}

// validateGlobalConfig consumes a yml config string with adapter config.
// It is validated in presence of validators.
func (p *validator) validateGlobalConfig(cfg string) (ce *adapter.ConfigErrors) {
	var m = &pb.GlobalConfig{}
	if err := yaml.Unmarshal([]byte(cfg), m); err != nil {
		return ce.Appendf("GlobalConfig", "failed to unmarshal config into proto with err: %v", err)
	}

	p.validated.adapterByName = make(map[adapterKey]*pb.Adapter)
	var acfg adapter.Config
	var err *adapter.ConfigErrors
	for _, aa := range m.GetAdapters() {
		if acfg, err = convertAdapterParams(p.adapterFinder, aa.Impl, aa.Params, p.strict); err != nil {
			ce = ce.Appendf("Adapter: "+aa.Impl, "failed to convert aspect params to proto with err: %v", err)
			continue
		}
		aa.Params = acfg
		// check which kinds aa.Impl provides
		// Then register it for all of them.
		kinds := p.findAspects(aa.Impl)
		for kind := Kind(0); kind < NumKinds; kind++ {
			if kinds.IsSet(kind) {
				p.validated.adapterByName[adapterKey{kind, aa.Name}] = aa
			}
		}
	}
	p.validated.globalConfig = m
	return
}

// ValidateSelector ensures that the selector is valid per expression language.
func (p *validator) validateSelector(selector string) (err error) {
	// empty selector always selects
	if len(selector) == 0 {
		return nil
	}
	return p.exprValidator.Validate(selector)
}

// validateAspectRules validates the recursive configuration data structure.
// It is primarily used by validate ServiceConfig.
func (p *validator) validateAspectRules(rules []*pb.AspectRule, path string, validatePresence bool) (ce *adapter.ConfigErrors) {
	var acfg adapter.Config
	for _, rule := range rules {
		if err := p.validateSelector(rule.GetSelector()); err != nil {
			ce = ce.Append(path+":Selector "+rule.GetSelector(), err)
		}
		var err *adapter.ConfigErrors
		path = path + "/" + rule.GetSelector()
		for idx, aa := range rule.GetAspects() {
			if acfg, err = convertAspectParams(p.managerFinder, aa.Kind, aa.GetParams(), p.strict, p.descriptorFinder); err != nil {
				ce = ce.Appendf(fmt.Sprintf("%s:%s[%d]", path, aa.Kind, idx), "failed to parse params with err: %v", err)
				continue
			}
			aa.Params = acfg
			p.validated.numAspects++
			if validatePresence {
				if aa.Adapter == "" {
					aa.Adapter = "default"
				}
				// ensure that aa.Kind has a registered adapter
				k, ok := ParseKind(aa.Kind)
				if !ok {
					ce = ce.Appendf("Kind", "%s is not a valid kind", aa.Kind)
				} else {
					ak := adapterKey{k, aa.Adapter}
					if p.validated.adapterByName[ak] == nil {
						ce = ce.Appendf("NamedAdapter", "%s not available", ak)
					}
				}
			}
		}
		rs := rule.GetRules()
		if len(rs) == 0 {
			continue
		}
		if verr := p.validateAspectRules(rs, path, validatePresence); verr != nil {
			ce = ce.Extend(verr)
		}
	}
	return ce
}

// validate validates a single serviceConfig and globalConfig together.
// It returns a fully validated Config if no errors are found.
func (p *validator) validate(serviceCfg string, globalCfg string) (rt *Validated, ce *adapter.ConfigErrors) {
	if re := p.validateGlobalConfig(globalCfg); re != nil {
		return rt, ce.Appendf("GlobalConfig", "failed validation").Extend(re)
	}
	// The order is important here, because serviceConfig refers to global config
	p.descriptorFinder = descriptor.NewFinder(p.validated.globalConfig)

	if re := p.validateServiceConfig(serviceCfg, true); re != nil {
		return rt, ce.Appendf("ServiceConfig", "failed validation").Extend(re)
	}
	return p.validated, nil
}

// ValidateServiceConfig validates service config.
// if validatePresence is true it will ensure that the named adapter and Kinds
// have an available and configured adapter.
func (p *validator) validateServiceConfig(cfg string, validatePresence bool) (ce *adapter.ConfigErrors) {
	var err error
	m := &pb.ServiceConfig{}
	if err = yaml.Unmarshal([]byte(cfg), m); err != nil {
		return ce.Appendf("ServiceConfig", "failed to unmarshal config into proto with err: %v", err)
	}
	if ce = p.validateAspectRules(m.GetRules(), "", validatePresence); ce != nil {
		return ce
	}
	p.validated.serviceConfig = m
	return
}

// unknownValidator returns error for the given name.
func unknownValidator(name string) error {
	return fmt.Errorf("unknown type [%s]", name)
}

// unknownKind returns error for the given name.
func unknownKind(name string) error {
	return fmt.Errorf("unknown aspect kind [%s]", name)
}

// convertAdapterParams converts returns a typed proto message based on available validator.
func convertAdapterParams(f BuilderValidatorFinder, name string, params interface{}, strict bool) (ac adapter.Config, ce *adapter.ConfigErrors) {
	var avl adapter.ConfigValidator
	var found bool

	if avl, found = f(name); !found {
		return nil, ce.Append(name, unknownValidator(name))
	}

	ac = avl.DefaultConfig()
	if err := decode(params, ac, strict); err != nil {
		return nil, ce.Appendf(name, "failed to decode adapter params with err: %v", err)
	}
	if err := avl.ValidateConfig(ac); err != nil {
		return nil, ce.Appendf(name, "adapter validation failed with err: %v", err)
	}
	return ac, nil
}

// convertAspectParams converts returns a typed proto message based on available validator.
func convertAspectParams(f AspectValidatorFinder, name string, params interface{}, strict bool, df descriptor.Finder) (AspectParams, *adapter.ConfigErrors) {
	var ce *adapter.ConfigErrors
	var avl AspectValidator
	var found bool
	var k Kind

	if k, found = ParseKind(name); !found {
		return nil, ce.Append(name, unknownKind(name))
	}

	if avl, found = f(k); !found {
		return nil, ce.Append(name, unknownValidator(name))
	}

	ap := avl.DefaultConfig()
	if err := decode(params, ap, strict); err != nil {
		return nil, ce.Appendf(name, "failed to decode aspect params with err: %v", err)
	}
	if err := avl.ValidateConfig(ap, expr.NewCEXLEvaluator(), df); err != nil {
		return nil, ce.Appendf(name, "aspect validation failed with err: %v", err)
	}
	return ap, nil
}

// decode interprets src interface{} as the specified proto message.
// if strict is true returns error on unknown fields.
func decode(src interface{}, dst adapter.Config, strict bool) error {
	ba, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("failed to marshal config into json with err: %v", err)
	}
	um := jsonpb.Unmarshaler{AllowUnknownFields: !strict}
	if err := um.Unmarshal(bytes.NewReader(ba), dst); err != nil {
		return fmt.Errorf("failed to unmarshal config into proto with err: %v", err)
	}
	return nil
}
