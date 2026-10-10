package format

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/pin"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONHostnameOnlyForProximaDotcomDependencies(t *testing.T) {
	for _, tt := range []struct {
		name, homeHost, depHost, want string
	}{
		{"dotcom", "github.com", "github.com", ""},
		{"tenant local", "tenant.ghe.com", "tenant.ghe.com", ""},
		{"tenant public", "tenant.ghe.com", "github.com", "github.com"},
		{"unresolved", "tenant.ghe.com", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := &checks.Report{Workflows: []checks.WorkflowReport{{
				Path: ".github/workflows/ci.yml",
				Inventory: []checks.InventoryEntry{{
					Dep:    dep.Dependency{Hostname: tt.depHost, NWO: "o/r", Ref: "v1", SHA: "abc"},
					Direct: true,
				}},
			}}}
			var out bytes.Buffer
			require.NoError(t, WriteJSON(&out, report, nil, true, "dependencies,workflows", "dev", "v0.0.3", tt.homeHost))
			var payload struct {
				Dependencies []map[string]any `json:"dependencies"`
				Workflows    []struct {
					Dependencies []map[string]any `json:"dependencies"`
				} `json:"workflows"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
			require.Len(t, payload.Dependencies, 1)
			require.Len(t, payload.Workflows, 1)
			require.Len(t, payload.Workflows[0].Dependencies, 1)
			for _, entry := range []map[string]any{payload.Dependencies[0], payload.Workflows[0].Dependencies[0]} {
				if tt.want == "" {
					assert.NotContains(t, entry, "hostname")
				} else {
					assert.Equal(t, tt.want, entry["hostname"])
				}
			}
			assert.Equal(t, tt.depHost, report.Workflows[0].Inventory[0].Dep.Hostname)
		})
	}
}

func TestValidateJSONFields(t *testing.T) {
	tests := []struct {
		name    string
		fields  string
		wantErr bool
	}{
		{name: "empty is allowed", fields: "", wantErr: false},
		{name: "single valid field", fields: "findings", wantErr: false},
		{name: "all valid fields", fields: "valid,findings,workflows,dependencies", wantErr: false},
		{name: "valid fields with surrounding spaces", fields: " valid , findings ", wantErr: false},
		{name: "unknown field is rejected", fields: "foo", wantErr: true},
		{name: "unknown field mixed with valid", fields: "findings,foo", wantErr: true},
		{name: "empty segment from trailing comma is rejected", fields: "findings,", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJSONFields(tt.fields)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateJSONFields(%q) = nil, want error", tt.fields)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateJSONFields(%q) = %v, want nil", tt.fields, err)
			}
		})
	}
}

func TestPinsFromRecordBlockedWorkflow(t *testing.T) {
	report := &checks.Report{Workflows: []checks.WorkflowReport{
		{Path: "blocked.yml", SkipCommit: true},
		{Path: "ok.yml"},
	}}
	record := &pin.Record{Entries: []pin.Entry{
		{NWO: "o/only-blocked", Ref: "v1", SHA: "a", Resolution: pin.Verified, Workflows: []string{"blocked.yml"}},
		{NWO: "o/shared", Ref: "v1", SHA: "b", Resolution: pin.Verified, Workflows: []string{"blocked.yml"}},
		{NWO: "o/shared", Ref: "v1", SHA: "b", Resolution: pin.Verified, Workflows: []string{"ok.yml"}},
		{NWO: "o/fresh", Ref: "v1", SHA: "c", Resolution: pin.Pinned, Workflows: []string{"ok.yml"}},
	}}

	got := map[string]string{}
	for _, p := range pinsFromRecord(record, report) {
		got[p.NWO] = p.Outcome
	}

	assert.Equal(t, "skipped", got["o/only-blocked"], "pin used only by a blocked workflow was not checked")
	assert.Equal(t, "verified", got["o/shared"], "pin also used by a healthy workflow was checked")
	assert.Equal(t, "planned", got["o/fresh"])
}

func TestPinsFromRecord_DistinguishesPlannedFromApplied(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "planned"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			record := &pin.Record{Committed: committed, Entries: []pin.Entry{
				{NWO: "owner/new", Ref: "v1", Resolution: pin.Pinned, Workflows: []string{"ci.yml"}},
				{NWO: "owner/narrowed", Ref: "v2.0.0", AutoFixedRef: "v2", Resolution: pin.Verified, Workflows: []string{"ci.yml"}},
				{NWO: "owner/unchanged", Ref: "v3", Resolution: pin.Verified, Workflows: []string{"ci.yml"}},
			}}
			got := pinsFromRecord(record, nil)
			require.Len(t, got, 3)
			if committed {
				assert.Equal(t, "pinned", got[0].Outcome)
				assert.Equal(t, "verified", got[1].Outcome)
			} else {
				assert.Equal(t, "planned", got[0].Outcome)
				assert.Equal(t, "planned", got[1].Outcome)
			}
			assert.Equal(t, committed, got[0].Applied)
			assert.Equal(t, committed, got[1].Applied)
			assert.Equal(t, "v2", got[1].NarrowedFrom)
			assert.Equal(t, "verified", got[2].Outcome)
			assert.False(t, got[2].Applied, "checking an unchanged pin is not a file mutation")
		})
	}
}

func TestPinsFromRecordMergesDuplicateOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name         string
		committed    bool
		entries      []pin.Entry
		outcome      string
		applied      bool
		sha          string
		narrowedFrom string
		observedSHA  string
		reason       string
	}{
		{
			name: "applied pin overrides verified", committed: true,
			entries: []pin.Entry{{Resolution: pin.Verified, SHA: "old"}, {Resolution: pin.Pinned, SHA: "new"}},
			outcome: "pinned", applied: true, sha: "new",
		},
		{
			name:    "planned pin overrides verified",
			entries: []pin.Entry{{Resolution: pin.Verified, SHA: "old"}, {Resolution: pin.Pinned, SHA: "new"}},
			outcome: "planned", sha: "new",
		},
		{
			name: "applied narrowing overrides unchanged", committed: true,
			entries: []pin.Entry{{Resolution: pin.Verified}, {Resolution: pin.Verified, AutoFixedRef: "v1", Reason: "narrowed to patch tag"}},
			outcome: "verified", applied: true, narrowedFrom: "v1", reason: "narrowed to patch tag",
		},
		{
			name:    "planned narrowing overrides unchanged",
			entries: []pin.Entry{{Resolution: pin.Verified}, {Resolution: pin.Verified, AutoFixedRef: "v1", Reason: "narrowed to patch tag"}},
			outcome: "planned", narrowedFrom: "v1", reason: "narrowed to patch tag",
		},
		{
			name: "pin retains another workflow's narrowing", committed: true,
			entries: []pin.Entry{{Resolution: pin.Pinned, SHA: "new"}, {Resolution: pin.Verified, SHA: "new", AutoFixedRef: "v1"}},
			outcome: "pinned", applied: true, sha: "new", narrowedFrom: "v1",
		},
		{
			name: "investigation retains applied change", committed: true,
			entries: []pin.Entry{
				{Resolution: pin.Pinned, SHA: "new"},
				{Resolution: pin.Investigate, SHA: "old", ObservedSHA: "moved", Reason: "ref moved"},
			},
			outcome: "needs-investigation", applied: true, sha: "old", observedSHA: "moved", reason: "ref moved",
		},
		{
			name: "unresolved is not hidden by planned narrowing",
			entries: []pin.Entry{
				{Resolution: pin.Verified, AutoFixedRef: "v1"},
				{Resolution: pin.Unresolved, Reason: "resolution failed"},
			},
			outcome: "unresolved", narrowedFrom: "v1", reason: "resolution failed",
		},
		{
			name:    "verified overrides skipped",
			entries: []pin.Entry{{Resolution: pin.Skipped, Reason: "workflow skipped"}, {Resolution: pin.Verified}},
			outcome: "verified",
		},
		{
			name: "unresolved overrides investigation",
			entries: []pin.Entry{
				{Resolution: pin.Investigate, ObservedSHA: "moved", Reason: "ref moved"},
				{Resolution: pin.Unresolved, Reason: "resolution failed"},
			},
			outcome: "unresolved", reason: "resolution failed",
		},
		{
			name: "equal outcomes retain diagnosis",
			entries: []pin.Entry{
				{Resolution: pin.Investigate},
				{Resolution: pin.Investigate, ObservedSHA: "moved", Reason: "ref moved"},
			},
			outcome: "needs-investigation", observedSHA: "moved", reason: "ref moved",
		},
		{
			name: "equal diagnoses retain observed SHA",
			entries: []pin.Entry{
				{Resolution: pin.Investigate, Reason: "ref moved"},
				{Resolution: pin.Investigate, ObservedSHA: "moved", Reason: "ref moved"},
			},
			outcome: "needs-investigation", observedSHA: "moved", reason: "ref moved",
		},
		{
			name:    "equal outcomes choose a stable SHA",
			entries: []pin.Entry{{Resolution: pin.Verified, SHA: "aaa"}, {Resolution: pin.Verified, SHA: "bbb"}},
			outcome: "verified", sha: "bbb",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				entries := append([]pin.Entry(nil), tt.entries...)
				workflowPaths := []string{"ci.yml", "release.yml"}
				for i := range entries {
					entries[i].Hostname = "github.com"
					entries[i].NWO = "owner/action"
					entries[i].Ref = "v1.2.3"
					entries[i].Workflows = []string{workflowPaths[i]}
				}
				if reverse {
					entries[0], entries[1] = entries[1], entries[0]
				}
				var out bytes.Buffer
				record := &pin.Record{Committed: tt.committed, Entries: entries}
				require.NoError(t, WriteJSON(&out, &checks.Report{}, record, true, "pins", "dev", "v0.0.3", "github.com"))
				var payload struct {
					Pins []Pin `json:"pins"`
				}
				require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
				got := payload.Pins
				require.Len(t, got, 1)
				assert.Equal(t, tt.outcome, got[0].Outcome, "reverse=%t", reverse)
				assert.Equal(t, tt.applied, got[0].Applied, "reverse=%t", reverse)
				assert.Equal(t, tt.sha, got[0].SHA, "reverse=%t", reverse)
				assert.Equal(t, tt.narrowedFrom, got[0].NarrowedFrom, "reverse=%t", reverse)
				assert.Equal(t, tt.observedSHA, got[0].ObservedSHA, "reverse=%t", reverse)
				assert.Equal(t, tt.reason, got[0].Reason, "reverse=%t", reverse)
			}
		})
	}
}
