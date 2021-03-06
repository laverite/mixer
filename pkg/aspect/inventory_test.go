// Copyright 2017 The Istio Authors.
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
	"testing"
)

func TestInventory(t *testing.T) {
	// not much we can do here, just call and make sure it doesn't crash
	// and that we get something back...
	inventory := Inventory()

	if len(inventory.Preprocess) != 0 {
		t.Errorf("Found %d PreprocessManagers, wanted 0", len(inventory.Preprocess))
	}

	if len(inventory.Check) == 0 {
		t.Error("Expecting some managers for Check, got 0")
	}

	if len(inventory.Report) == 0 {
		t.Error("Expecting some managers for Repor, got 0")
	}

	if len(inventory.Quota) == 0 {
		t.Error("Expecting some managers for Quota, got 0")
	}
}
