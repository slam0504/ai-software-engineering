// Q1（reviewer 二次審查，2026-09-15）：`lsof` 一行輸出裡同時可能出現本機端與
// 遠端兩個位址（`本機->遠端`），先前直接對整行套 loopback regex 判斷——如果
// 本機端位址剛好含 "127"，會讓遠端其實是外部位址的連線被誤判成 loopback、
// 因而放行。這裡改成純函式：先抓出 NODE（TCP／UDP）欄位之後的位址／狀態，
// 再依連線種類（已連線／LISTEN／兩者都不是）分別解析，只看真正該看的那個
// 位址。純函式、無 I/O，方便獨立測試（見 networkLineParser.selftest.ts）。
export type ParsedNetworkLine =
  | { kind: 'connected'; protocol: string; localAddr: string; remoteAddr: string; remoteHost: string; violation: boolean; raw: string }
  | { kind: 'listen'; protocol: string; localAddr: string; localHost: string; violation: boolean; raw: string }
  | { kind: 'no-remote'; raw: string } // 沒有遠端也不是 LISTEN（例如 `CLOSED *:*`、未連線的 UDP）——只能證明「這一刻沒觀察到遠端」，不能證明「沒有外連過」。
  | { kind: 'unparseable'; raw: string };

// 抓 `TCP`／`UDP` 之後的位址＋（可能有的）狀態。lsof 的 NAME 欄位格式是
// `<addr>` 或 `<addr> (STATE)`；`<addr>` 本身不含空白，所以「NODE 之後到行尾」
// 就是完整的位址＋狀態。
// 狀態不是只有純大寫字母——TCP 狀態機還有 `SYN_SENT`／`TIME_WAIT`／
// `CLOSE_WAIT`／`FIN_WAIT_1`／`FIN_WAIT_2`／`LAST_ACK` 這些帶底線／數字的名稱
// （施工中實測 controls 那批連跑時真的撞到 `SYN_SENT`，先前只允許
// `[A-Z]+`，把這種合法狀態誤判成 unparseable）。
const NODE_ADDR_RE = /\b(TCP|UDP)\s+(\S+)(?:\s+\(([A-Z0-9_]+)\))?\s*$/;

function stripBrackets(host: string): string {
  return host.startsWith('[') && host.endsWith(']') ? host.slice(1, -1) : host;
}

// 把 `host:port` 拆開；IPv6 位址用 `[host]:port` 包起來，其餘（IPv4／`*`）用
// 最後一個 `:` 切開即可（IPv4 與 `*` 都不含冒號）。
function splitHostPort(addr: string): { host: string; port: string } {
  if (addr.startsWith('[')) {
    const m = /^\[([^\]]*)\]:(.*)$/.exec(addr);
    if (m) return { host: stripBrackets(`[${m[1]}]`), port: m[2] };
  }
  const idx = addr.lastIndexOf(':');
  if (idx === -1) return { host: addr, port: '' };
  return { host: addr.slice(0, idx), port: addr.slice(idx + 1) };
}

export function isLoopbackHost(host: string): boolean {
  const h = stripBrackets(host);
  if (h === 'localhost') return true;
  if (h === '::1') return true;
  if (/^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(h)) return true;
  return false;
}

// parseNetworkLine：treatLoopbackAsExternal 是 N9a 注入點的判定邏輯本身
// （把 loopback 也當外連，用來驗證判定機制真的有作用）——只對「有位址可判斷」
// 的 connected／listen 兩種生效；no-remote／unparseable 沒有位址可比對，不受
// 這個旗標影響（見型別註解）。
export function parseNetworkLine(rawLine: string, treatLoopbackAsExternal: boolean): ParsedNetworkLine {
  const raw = rawLine;
  const m = NODE_ADDR_RE.exec(rawLine);
  if (!m) return { kind: 'unparseable', raw };
  const [, protocol, addrPart, state] = m;

  if (addrPart.includes('->')) {
    const arrowIdx = addrPart.indexOf('->');
    const localAddr = addrPart.slice(0, arrowIdx);
    const remoteAddr = addrPart.slice(arrowIdx + 2);
    const remoteHost = splitHostPort(remoteAddr).host;
    const violation = treatLoopbackAsExternal || !isLoopbackHost(remoteHost);
    return { kind: 'connected', protocol, localAddr, remoteAddr, remoteHost, violation, raw };
  }

  if (state === 'LISTEN') {
    const localHost = splitHostPort(addrPart).host;
    const violation = treatLoopbackAsExternal || !isLoopbackHost(localHost);
    return { kind: 'listen', protocol, localAddr: addrPart, localHost, violation, raw };
  }

  // 沒有 `->`、也不是 LISTEN：例如 `CLOSED *:*`、未連線的 UDP。只記為診斷，
  // 不能宣稱「沒有外連過」——这一刻只是沒觀察到遠端而已。
  return { kind: 'no-remote', raw };
}
