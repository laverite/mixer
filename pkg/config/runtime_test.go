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

package config

import (
	"errors"
	"flag"
	"testing"

	multierror "github.com/hashicorp/go-multierror"

	"istio.io/mixer/pkg/attribute"
	pb "istio.io/mixer/pkg/config/proto"
)

type trueEval struct {
	err    error
	ncalls int
	ret    bool
}

func (t *trueEval) EvalPredicate(expression string, attrs attribute.Bag) (bool, error) {
	if t.ncalls == 0 {
		return t.ret, t.err
	}
	t.ncalls--
	return true, nil
}

type ttable struct {
	err    error
	ncalls int
	ret    bool
	nlen   int
	asp    []string
}

func TestRuntime(t *testing.T) {
	table := []*ttable{
		{nil, 0, true, 4, []string{ListsKindName}},
		{nil, 1, false, 2, []string{ListsKindName}},
		{errors.New("predicate error"), 1, false, 2, []string{ListsKindName}},
		{nil, 0, true, 0, []string{}},
		{errors.New("predicate error"), 0, true, 0, []string{ListsKindName}},
	}

	LC := ListsKindName
	a1 := &pb.Adapter{
		Name: "a1",
		Kind: LC,
	}
	a2 := &pb.Adapter{
		Name: "a2",
		Kind: LC,
	}

	v := &Validated{
		adapterByName: map[adapterKey]*pb.Adapter{
			{ListsKind, "a1"}: a1,
			{ListsKind, "a2"}: a2,
		},
		serviceConfig: &pb.ServiceConfig{
			Rules: []*pb.AspectRule{
				{
					Selector: "ok",
					Aspects: []*pb.Aspect{
						{
							Kind: LC,
						},
						{
							Adapter: "a2",
							Kind:    LC,
						},
					},
					Rules: []*pb.AspectRule{
						{
							Selector: "ok",
							Aspects: []*pb.Aspect{
								{
									Kind: LC,
								},
								{
									Adapter: "a2",
									Kind:    LC,
								},
							},
						},
					},
				},
			},
		},
		numAspects: 1,
	}

	bag := attribute.GetMutableBag(nil)

	for idx, tt := range table {
		fe := &trueEval{tt.err, tt.ncalls, tt.ret}
		var kinds KindSet
		for _, a := range tt.asp {
			k, _ := ParseKind(a)
			kinds = kinds.Set(k)
		}
		rt := newRuntime(v, fe)

		al, err := rt.Resolve(bag, kinds)

		if tt.err != nil {
			merr := err.(*multierror.Error)
			if merr.Errors[0] != tt.err {
				t.Error(idx, "expected:", tt.err, "\ngot:", merr.Errors[0])
			}
		}

		if len(al) != tt.nlen {
			t.Errorf("%d Expected %d resolve got %d", idx, tt.nlen, len(al))
		}
	}
}

func TestRuntime_ResolveUnconditional(t *testing.T) {
	table := []*ttable{
		{nil, 0, true, 2, []string{AttributeGenerationKindName}},
		{nil, 0, true, 0, []string{}},
	}

	LC := ListsKindName
	a1 := &pb.Adapter{
		Name: "a1",
		Kind: LC,
	}
	a2 := &pb.Adapter{
		Name: "a2",
		Kind: LC,
	}
	ag := &pb.Adapter{
		Name: "ag",
		Kind: AttributeGenerationKindName,
	}

	v := &Validated{
		adapterByName: map[adapterKey]*pb.Adapter{
			{ListsKind, "a1"}:               a1,
			{ListsKind, "a2"}:               a2,
			{AttributeGenerationKind, "ag"}: ag,
		},
		serviceConfig: &pb.ServiceConfig{
			Rules: []*pb.AspectRule{
				{
					Selector: "ok",
					Aspects: []*pb.Aspect{
						{
							Kind: LC,
						},
						{
							Adapter: "a2",
							Kind:    LC,
						},
					},
					Rules: []*pb.AspectRule{
						{
							Selector: "ok",
							Aspects: []*pb.Aspect{
								{
									Kind: LC,
								},
								{
									Adapter: "a2",
									Kind:    LC,
								},
							},
						},
					},
				},
				{
					Selector: "",
					Aspects: []*pb.Aspect{
						{
							Kind: AttributeGenerationKindName,
						},
						{
							Adapter: "ag",
							Kind:    AttributeGenerationKindName,
						},
					},
				},
			},
		},
		numAspects: 2,
	}

	bag := attribute.GetMutableBag(nil)

	for idx, tt := range table {
		fe := &trueEval{tt.err, tt.ncalls, tt.ret}
		var kinds KindSet
		for _, a := range tt.asp {
			k, _ := ParseKind(a)
			kinds = kinds.Set(k)
		}
		rt := newRuntime(v, fe)

		al, err := rt.ResolveUnconditional(bag, kinds)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if len(al) != tt.nlen {
			t.Errorf("[%d] Expected %d resolves got %d", idx, tt.nlen, len(al))
		}

		for _, cfg := range al {
			if cfg.Aspect.Kind != AttributeGenerationKindName {
				t.Errorf("Got aspect kind: %v, want %v", cfg.Aspect.Kind, AttributeGenerationKindName)
			}
		}
	}
}

func init() {
	// bump up the log level so log-only logic runs during the tests, for correctness and coverage.
	_ = flag.Lookup("v").Value.Set("99")
}
