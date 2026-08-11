// localStorage 包裝（UI 偏好記憶：Timeline 高度／摺疊）。讀取失敗回 fallback。
export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

export function save(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // storage 不可用（如隱私模式）：偏好不持久化，不影響功能
  }
}
