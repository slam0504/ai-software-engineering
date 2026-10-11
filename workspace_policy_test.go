package main

import (
	"reflect"
	"testing"
)

// ---- B4 R1：workspacePolicy（純函式）----

func knownHome() workspaceProvenance { return workspaceProvenance{HomeIdentityKnown: true} }

func TestWorkspacePolicy(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		prov     workspaceProvenance
		required bool
		reasons  []string
	}{
		{"env 正常、身分可判定：不需確認", "env", knownHome(), false, []string{}},
		{"cwd 正常：不需確認（wails dev、從專案目錄啟動）", "cwd", knownHome(), false, []string{}},
		{"(a) source=home", "home", knownHome(), true, []string{wsReasonSourceHome}},
		{"(a) source=tmp", "tmp", knownHome(), true, []string{wsReasonSourceTmp}},
		{"(b) env 無效退回 cwd（normalize 失敗）", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectNormalizeFailed, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejectedNormalize}},
		{"(b) env 無效退回 cwd（mkdir 失敗）", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectMkdirFailed, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejectedMkdir}},
		{"(b) 未知的拒絕原因仍算被拒", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejected}},
		{"(b) 必須 envSet 與 envRejected 同時成立", "cwd",
			workspaceProvenance{EnvRejected: true, EnvRejectReason: envRejectMkdirFailed, HomeIdentityKnown: true},
			false, []string{}},
		{"(c) isHome 與 source 無關（env=$HOME）", "env",
			workspaceProvenance{EnvSet: true, IsHome: true, HomeIdentityKnown: true},
			true, []string{wsReasonIsHome}},
		{"(c) cd ~ 後啟動（source=cwd）", "cwd",
			workspaceProvenance{IsHome: true, HomeIdentityKnown: true},
			true, []string{wsReasonIsHome}},
		{"(d) 家目錄身分無法判定：即使 env 正常也要確認", "env",
			workspaceProvenance{EnvSet: true}, true, []string{wsReasonHomeUnknown}},
		{"家目錄底下的專案子目錄：isHome=false、身分可判定 → 不需確認（Invariant）", "env",
			workspaceProvenance{EnvSet: true, IsHome: false, HomeIdentityKnown: true}, false, []string{}},
		{"多個原因同時成立：全部列出、順序固定 a→b→c→d", "home",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectNormalizeFailed, IsHome: true},
			true, []string{wsReasonSourceHome, wsReasonEnvRejectedNormalize, wsReasonIsHome, wsReasonHomeUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			required, reasons := workspacePolicy(c.source, c.prov)
			if required != c.required {
				t.Fatalf("required=%v，want %v（reasons=%v）", required, c.required, reasons)
			}
			if !reflect.DeepEqual(reasons, c.reasons) {
				t.Fatalf("reasons=%#v，want %#v", reasons, c.reasons)
			}
		})
	}
}
