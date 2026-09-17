// harness.log 是唯一保證「啟動失敗也一定有」的證據（§2.9）。從預檢第一步就要
// 能寫，所以這個模組除了 Node 內建 fs 不依賴任何在預檢之前才會就緒的東西。
import fs from 'node:fs';
import path from 'node:path';

export class HarnessLogger {
  private readonly file: string;

  constructor(artifactsDir: string) {
    fs.mkdirSync(artifactsDir, { recursive: true });
    this.file = path.join(artifactsDir, 'harness.log');
  }

  log(msg: string): void {
    const line = `[${new Date().toISOString()}] ${msg}\n`;
    fs.appendFileSync(this.file, line);
    // eslint-disable-next-line no-console
    console.log(`[harness] ${msg}`);
  }

  path(): string {
    return this.file;
  }
}
