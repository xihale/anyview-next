import { describe, expect, it } from "vitest";
import { b64encodeUtf8, decodeStuCode, fmtClock } from "~/composables/useApi";

describe("b64encodeUtf8 / decodeStuCode", () => {
  it("ASCII 往返一致", () => {
    const s = "int main() {\n  return 0;\n}";
    expect(decodeStuCode(b64encodeUtf8(s))).toBe(s);
  });

  it("多字节(中文/emoji)经 UTF-8 往返不乱码", () => {
    const s = "// 中文注释 ✓\nprintf(\"你好\");\n😀";
    expect(decodeStuCode(b64encodeUtf8(s))).toBe(s);
  });

  it("超 0x8000 字节分块编码不炸(栈溢出防护)", () => {
    const s = "a".repeat(0x8000 * 3 + 7);
    expect(decodeStuCode(b64encodeUtf8(s))).toBe(s);
  });

  it("decodeStuCode 对明文/空串原样返回", () => {
    expect(decodeStuCode("")).toBe("");
    expect(decodeStuCode("plain code")).toBe("plain code");
  });
});

describe("fmtClock", () => {
  it("秒/分/时边界", () => {
    expect(fmtClock(0)).toBe("00:00");
    expect(fmtClock(59)).toBe("00:59");
    expect(fmtClock(60)).toBe("01:00");
    expect(fmtClock(3599)).toBe("59:59");
    expect(fmtClock(3600)).toBe("1:00:00");
    expect(fmtClock(7359)).toBe("2:02:39");
  });

  it("负数与小数钳制", () => {
    expect(fmtClock(-5)).toBe("00:00");
    expect(fmtClock(90.9)).toBe("01:30");
  });
});
