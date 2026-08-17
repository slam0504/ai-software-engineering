export namespace contract {
	
	export class Binding {
	    kind: string;
	    ref: string;
	    digest: string;
	
	    static createFrom(source: any = {}) {
	        return new Binding(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.ref = source["ref"];
	        this.digest = source["digest"];
	    }
	}
	export class Usage {
	    input_tokens: number;
	    output_tokens: number;
	    cached_input_tokens?: number;
	
	    static createFrom(source: any = {}) {
	        return new Usage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cached_input_tokens = source["cached_input_tokens"];
	    }
	}
	export class Envelope {
	    event_id: string;
	    ts: string;
	    provider: string;
	    session_id?: string;
	    role?: string;
	    task_id?: string;
	    kind: string;
	    text?: string;
	    thinking?: string;
	    is_error?: boolean;
	    cost_usd?: number;
	    usage?: Usage;
	    usage_semantics?: string;
	    state?: string;
	    error?: string;
	    raw?: number[];
	    scope?: string;
	    bindings?: Binding[];
	    payload?: number[];
	    correlation_id?: string;
	    purpose?: string;
	    workspace_session_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new Envelope(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.event_id = source["event_id"];
	        this.ts = source["ts"];
	        this.provider = source["provider"];
	        this.session_id = source["session_id"];
	        this.role = source["role"];
	        this.task_id = source["task_id"];
	        this.kind = source["kind"];
	        this.text = source["text"];
	        this.thinking = source["thinking"];
	        this.is_error = source["is_error"];
	        this.cost_usd = source["cost_usd"];
	        this.usage = this.convertValues(source["usage"], Usage);
	        this.usage_semantics = source["usage_semantics"];
	        this.state = source["state"];
	        this.error = source["error"];
	        this.raw = source["raw"];
	        this.scope = source["scope"];
	        this.bindings = this.convertValues(source["bindings"], Binding);
	        this.payload = source["payload"];
	        this.correlation_id = source["correlation_id"];
	        this.purpose = source["purpose"];
	        this.workspace_session_id = source["workspace_session_id"];
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

export namespace escalation {
	
	export class Item {
	    _type: string;
	    escalation_id: string;
	    condition_key: string;
	    occurrence: number;
	    source: string;
	    source_ref: string;
	    block_scope: string;
	    hard: boolean;
	    summary: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this._type = source["_type"];
	        this.escalation_id = source["escalation_id"];
	        this.condition_key = source["condition_key"];
	        this.occurrence = source["occurrence"];
	        this.source = source["source"];
	        this.source_ref = source["source_ref"];
	        this.block_scope = source["block_scope"];
	        this.hard = source["hard"];
	        this.summary = source["summary"];
	        this.created_at = source["created_at"];
	    }
	}
	export class Entry {
	    Item: Item;
	    State: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Item = this.convertValues(source["Item"], Item);
	        this.State = source["State"];
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
	export class BumpToken {
	    plan_rel: string;
	    old: string;
	    head: string;
	    buffer_digest: string;
	
	    static createFrom(source: any = {}) {
	        return new BumpToken(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.plan_rel = source["plan_rel"];
	        this.old = source["old"];
	        this.head = source["head"];
	        this.buffer_digest = source["buffer_digest"];
	    }
	}
	export class BumpPreview {
	    token: BumpToken;
	    old: string;
	    head: string;
	    commits: CommitInfo[];
	    touched_files: string[];
	    no_bump_needed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BumpPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = this.convertValues(source["token"], BumpToken);
	        this.old = source["old"];
	        this.head = source["head"];
	        this.commits = this.convertValues(source["commits"], CommitInfo);
	        this.touched_files = source["touched_files"];
	        this.no_bump_needed = source["no_bump_needed"];
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
	export class PaneLayout {
	    pins: string[];
	    focused: string;
	
	    static createFrom(source: any = {}) {
	        return new PaneLayout(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pins = source["pins"];
	        this.focused = source["focused"];
	    }
	}
	export class SessionInfo {
	    wsid: string;
	    provider: string;
	    task_label: string;
	    resume_session_id: string;
	    created_at: string;
	    available: boolean;
	    state: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.wsid = source["wsid"];
	        this.provider = source["provider"];
	        this.task_label = source["task_label"];
	        this.resume_session_id = source["resume_session_id"];
	        this.created_at = source["created_at"];
	        this.available = source["available"];
	        this.state = source["state"];
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

