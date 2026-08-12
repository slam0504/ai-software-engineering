export namespace evidence {
	
	export class EvidenceRun {
	    evidence_id: string;
	    kind: string;
	    source: string;
	    base_commit: string;
	    test_commit: string;
	    oracle_surface_digest: string;
	    mutation_digest?: string;
	    command: plan.Command;
	    cwd: string;
	    started_at: string;
	    finished_at: string;
	    exit_code: number;
	    expected_failure: plan.ExpectedFailure;
	    observed_failure: string;
	    stdout_digest: string;
	    stderr_digest: string;
	    recording_ref: string;
	    runner_version: string;
	    result: string;
	
	    static createFrom(source: any = {}) {
	        return new EvidenceRun(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.evidence_id = source["evidence_id"];
	        this.kind = source["kind"];
	        this.source = source["source"];
	        this.base_commit = source["base_commit"];
	        this.test_commit = source["test_commit"];
	        this.oracle_surface_digest = source["oracle_surface_digest"];
	        this.mutation_digest = source["mutation_digest"];
	        this.command = this.convertValues(source["command"], plan.Command);
	        this.cwd = source["cwd"];
	        this.started_at = source["started_at"];
	        this.finished_at = source["finished_at"];
	        this.exit_code = source["exit_code"];
	        this.expected_failure = this.convertValues(source["expected_failure"], plan.ExpectedFailure);
	        this.observed_failure = source["observed_failure"];
	        this.stdout_digest = source["stdout_digest"];
	        this.stderr_digest = source["stderr_digest"];
	        this.recording_ref = source["recording_ref"];
	        this.runner_version = source["runner_version"];
	        this.result = source["result"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace gate {
	
	export class Approver {
	    id: string;
	    method: string;
	
	    static createFrom(source: any = {}) {
	        return new Approver(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.method = source["method"];
	    }
	}
	export class Binding {
	    kind: string;
	    role?: string;
	    ref: string;
	    digest: string;
	
	    static createFrom(source: any = {}) {
	        return new Binding(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.role = source["role"];
	        this.ref = source["ref"];
	        this.digest = source["digest"];
	    }
	}
	export class RiskSelection {
	    TaskID: string;
	    SelectedRiskTier: string;
	    OverrideReason: string;
	
	    static createFrom(source: any = {}) {
	        return new RiskSelection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.TaskID = source["TaskID"];
	        this.SelectedRiskTier = source["SelectedRiskTier"];
	        this.OverrideReason = source["OverrideReason"];
	    }
	}

}

export namespace main {
	
	export class CommitInfo {
	    oid: string;
	    subject: string;
	
	    static createFrom(source: any = {}) {
	        return new CommitInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.oid = source["oid"];
	        this.subject = source["subject"];
	    }
	}
	export class FileNode {
	    name: string;
	    path: string;
	    isDir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	    }
	}
	export class GateDecisionTaskDTO {
	    task_id: string;
	    title: string;
	    minimum_risk_tier: string;
	    planner_risk_tier: string;
	
	    static createFrom(source: any = {}) {
	        return new GateDecisionTaskDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.title = source["title"];
	        this.minimum_risk_tier = source["minimum_risk_tier"];
	        this.planner_risk_tier = source["planner_risk_tier"];
	    }
	}
	export class GateDecisionContextDTO {
	    tasks: GateDecisionTaskDTO[];
	
	    static createFrom(source: any = {}) {
	        return new GateDecisionContextDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tasks = this.convertValues(source["tasks"], GateDecisionTaskDTO);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class GateEntryDTO {
	    approval_id: string;
	    state: string;
	    gate?: string;
	    subject?: string;
	    spec_manifest_digest?: string;
	    base_commit?: string;
	    created_at?: string;
	    bindings?: gate.Binding[];
	    decision?: string;
	    reason?: string;
	    approver?: gate.Approver;
	    journal_degraded?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GateEntryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.approval_id = source["approval_id"];
	        this.state = source["state"];
	        this.gate = source["gate"];
	        this.subject = source["subject"];
	        this.spec_manifest_digest = source["spec_manifest_digest"];
	        this.base_commit = source["base_commit"];
	        this.created_at = source["created_at"];
	        this.bindings = this.convertValues(source["bindings"], gate.Binding);
	        this.decision = source["decision"];
	        this.reason = source["reason"];
	        this.approver = this.convertValues(source["approver"], gate.Approver);
	        this.journal_degraded = source["journal_degraded"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SpecCommitPreview {
	    token: spec.CommitToken;
	    diff: string;
	
	    static createFrom(source: any = {}) {
	        return new SpecCommitPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = this.convertValues(source["token"], spec.CommitToken);
	        this.diff = source["diff"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SpecFile {
	    content: string;
	    digest: string;
	
	    static createFrom(source: any = {}) {
	        return new SpecFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.digest = source["digest"];
	    }
	}

}

export namespace plan {
	
	export class Command {
	    executable: string;
	    argv: string[];
	
	    static createFrom(source: any = {}) {
	        return new Command(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.executable = source["executable"];
	        this.argv = source["argv"];
	    }
	}
	export class ExpectedFailure {
	    test_ids: string[];
	    matcher: string;
	
	    static createFrom(source: any = {}) {
	        return new ExpectedFailure(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.test_ids = source["test_ids"];
	        this.matcher = source["matcher"];
	    }
	}

}

export namespace spec {
	
	export class CommitToken {
	    HeadOID: string;
	    TreeDigest: string;
	    AnalysisBase: string;
	
	    static createFrom(source: any = {}) {
	        return new CommitToken(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.HeadOID = source["HeadOID"];
	        this.TreeDigest = source["TreeDigest"];
	        this.AnalysisBase = source["AnalysisBase"];
	    }
	}

}

