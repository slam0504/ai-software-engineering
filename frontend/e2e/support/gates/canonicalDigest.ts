// B3a-2a：Gate 2 四個 binding digest 的 TS 側獨立重算——鏡射
// internal/spec/scope.go 的 Scope.ManifestDigest／internal/spec/manifest.go
// 的 HashBytes 公式（design v4 §7.3）。純函式，不依賴 Playwright、不依賴
// 待驗的 App 輸出／journal 反填，只依賴 fixture builder 自己知道已經寫了
// 什麼內容。
//
// **等價界線（design v4 §0 精確性修正 2，務必遵守）**：這裡的 TS 重算只在
// 受控 fixture 下與 Go 端等價——ASCII 路徑、非空 permission refs。已知的
// 不等價之處（用 /tmp Go 產生器實測驗證過，見 canonicalDigest.selftest.ts
// 的 special_chars／nil_files 兩個案例）：
//   1. Go 的 `encoding/json.Marshal` 預設會做 HTML-safe 轉義
//      （`<`→`<`、`>`→`>`、`&`→`&`），JS 的 `JSON.stringify`
//      不會。含這些字元的路徑，兩側算出的 canonical bytes 與 digest 不同。
//   2. Go 對 `nil` slice 序列化為 `null`；此模組傳入空陣列時
//      `JSON.stringify([])` 產出 `[]`，與 Go 的 `null` 不同——因此
//      `entries` 為空陣列時，呼叫端必須明確知道這個差異不成立等價（見
//      selftest `nil_files` 案例，只驗證各自的字面輸出，不宣稱一致）。
//   3. 排序：Go 用 byte-wise `<`（`sort.Slice` 比較 `string` 型別，Go
//      string 是 byte 序列）；這裡用 JS 預設 `Array.prototype.sort` 對
//      string 做 UTF-16 code unit 比較——ASCII 範圍內兩者結果一致，非
//      ASCII 範圍未驗證，不宣稱等價。
import { createHash } from 'node:crypto';

export interface CanonicalFileEntry {
  path: string;
  sha256: string; // hex，無前綴（對齊 Go 的 FileEntry.SHA256／HashBytes）
}

/** HashBytes 等價：純 hex sha256，無前綴。 */
export function hashBytes(content: string | Buffer): string {
  return createHash('sha256').update(content).digest('hex');
}

/**
 * ManifestDigest 等價：依 path 排序＋固定欄位順序（scope_version, patterns,
 * files）組 canonical JSON，sha256 後加 "sha256:" 前綴。只在受控 ASCII
 * fixture 下與 Go 端等價，見本檔頂部說明。
 */
export function manifestDigest(scopeVersion: number, patterns: readonly string[], entries: readonly CanonicalFileEntry[]): string {
  const files = [...entries].sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  const canonical = {
    scope_version: scopeVersion,
    patterns: [...patterns],
    files: files.map(f => ({ path: f.path, sha256: f.sha256 })),
  };
  const json = JSON.stringify(canonical);
  return `sha256:${createHash('sha256').update(json, 'utf8').digest('hex')}`;
}

/** 單檔案 digest 等價（risk_policy binding 用，非 Scope.ManifestDigest）。 */
export function singleFileDigest(content: string | Buffer): string {
  return `sha256:${hashBytes(content)}`;
}

/** SpecScope 的固定參數（design v4 §7.3，對照 internal/spec/manifest.go）。 */
export const SPEC_SCOPE_VERSION = 1;
export const SPEC_SCOPE_PATTERNS = ['spec/features/**', 'spec/nfr/**', 'spec/glossary.md', 'spec/context-map/**'] as const;

/** PlanScope 的固定參數（對照 internal/spec/scope.go:28-36）。 */
export const PLAN_SCOPE_VERSION = 1;
export const PLAN_SCOPE_PATTERNS = ['plan/**'] as const;

/** permissionManifestScope 的固定參數（對照 app.go:4264 附近）。 */
export const PERMISSION_MANIFEST_SCOPE_VERSION = 1;
export const PERMISSION_MANIFEST_SCOPE_PATTERNS = ['plan/permissions/**'] as const;

export function specManifestDigest(entries: readonly CanonicalFileEntry[]): string {
  return manifestDigest(SPEC_SCOPE_VERSION, SPEC_SCOPE_PATTERNS, entries);
}

export function planManifestDigest(entries: readonly CanonicalFileEntry[]): string {
  return manifestDigest(PLAN_SCOPE_VERSION, PLAN_SCOPE_PATTERNS, entries);
}

export function permissionManifestDigest(entries: readonly CanonicalFileEntry[]): string {
  return manifestDigest(PERMISSION_MANIFEST_SCOPE_VERSION, PERMISSION_MANIFEST_SCOPE_PATTERNS, entries);
}
