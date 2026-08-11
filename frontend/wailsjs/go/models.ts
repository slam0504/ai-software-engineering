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

}

export namespace main {
	
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
	export class GateEntryDTO {
	    approval_id: string;
	    state: string;
	    gate?: string;
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

}

