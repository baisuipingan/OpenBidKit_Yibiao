/**
 * 阶段空转检测。
 *
 * 什么是空转：
 *   Agent 反复做同样的事，但没有实质进展。典型表现：
 *   - 同一轮里重复调用同一个工具、参数几乎一样
 *   - 连续多轮都在读同一个文件
 *   - 反复尝试同一个失败的操作（如 bash 里输错命令）
 *
 * 为什么需要：
 *   长任务（正文生成 30+ 分钟）一旦空转，会一直烧 token 直到超时。
 *   实测见过 Agent 连续 30 多次 `bash` 去 grep 自己的源码找工具，
 *   每次都不产生任何产物。
 *
 * 与桌面版的差异：
 *   桌面版 piStallDiagnosis.cjs 会暂停原 Agent、启动诊断 Agent 读会话记录，
 *   给出「继续 / 停止 / 收尾」的结论。这套很重（诊断本身要 20 分钟）。
 *
 *   Web 版做**轻量版**：
 *     一旦检测到空转，直接注入一段「停止打转，按以下方式继续」的提示，
 *     让原 Agent 自己跳出来。覆盖 80% 的实际场景。
 */

/** 默认阈值：连续 N 轮相同模式判定为空转 */
const DEFAULT_REPEAT_THRESHOLD = 4;
/** 一轮里同一工具调用超过 N 次，判定为空转 */
const DEFAULT_SAME_TOOL_THRESHOLD = 8;

/**
 * 空转检测器。
 *
 * 用法：
 *   const detector = createStallDetector();
 *   // 每次工具调用后
 *   const verdict = detector.record({ tool, args, text });
 *   if (verdict.stalled) { 注入提示 }
 */
export function createStallDetector(options = {}) {
  const repeatThreshold = options.repeatThreshold ?? DEFAULT_REPEAT_THRESHOLD;
  const sameToolThreshold = options.sameToolThreshold ?? DEFAULT_SAME_TOOL_THRESHOLD;

  /** 历史工具调用签名（用于检测重复） */
  let signatures = [];
  /** 当前轮的工具调用（每轮 prompt 开始时重置） */
  let currentToolCalls = [];
  /** 已注入过几次提示 */
  let nudges = 0;

  return {
    /** 新一轮开始时调用，重置本轮计数 */
    startRound() {
      currentToolCalls = [];
    },

    /**
     * 记录一次工具调用，返回判定结果。
     *
     * @param {{tool: string, args: any, text?: string}} call
     * @returns {{stalled: boolean, reason?: string, shouldNudge: boolean, nudgeLevel: number}}
     */
    record(call) {
      const sig = signatureOf(call);
      currentToolCalls.push(sig);
      signatures.push(sig);

      // 只保留最近 40 条，避免无限增长
      if (signatures.length > 40) {
        signatures = signatures.slice(-40);
      }

      // 规则 1：一轮内同一工具被调用太多次
      const sameToolCount = currentToolCalls.filter(
        (s) => s.tool === call.tool,
      ).length;
      if (sameToolCount >= sameToolThreshold) {
        return {
          stalled: true,
          reason: `本轮已调用 ${sameToolCount} 次 ${call.tool}，没有实质进展`,
          shouldNudge: true,
          nudgeLevel: 2,
        };
      }

      // 规则 2：连续重复同一签名
      const tail = signatures.slice(-repeatThreshold);
      if (
        tail.length >= repeatThreshold &&
        tail.every((s) => s.hash === sig.hash)
      ) {
        return {
          stalled: true,
          reason: `连续 ${repeatThreshold} 次重复同一操作（${call.tool}），参数几乎相同`,
          shouldNudge: true,
          nudgeLevel: 2,
        };
      }

      // 规则 3：连续重复同一工具+相似参数（不看完整签名）
      const looseTail = signatures.slice(-repeatThreshold);
      if (
        looseTail.length >= repeatThreshold &&
        looseTail.every((s) => s.tool === call.tool && s.looseHash === sig.looseHash)
      ) {
        return {
          stalled: true,
          reason: `连续 ${repeatThreshold} 次用相同方式调用 ${call.tool}`,
          shouldNudge: true,
          nudgeLevel: 1,
        };
      }

      return { stalled: false, shouldNudge: false, nudgeLevel: 0 };
    },

    /** 标记已注入提示，返回本次是第几次 */
    markNudged() {
      nudges += 1;
      return nudges;
    },

    /** 取已注入次数 */
    get nudgeCount() {
      return nudges;
    },

    /** 重置（阶段切换时用） */
    reset() {
      signatures = [];
      currentToolCalls = [];
      nudges = 0;
    },
  };
}

/**
 * 计算工具调用的签名。
 *
 * 用途：判断两次调用是否「实质相同」。
 *  - hash：完整参数的哈希（严格相同）
 *  - looseHash：只取关键参数的哈希（宽松相同，用于识别「换汤不换药」）
 */
function signatureOf(call) {
  const tool = String(call.tool || '');
  const args = call.args ?? {};

  return {
    tool,
    hash: hashString(tool + '|' + stableStringify(args)),
    looseHash: hashString(tool + '|' + looseArgKey(tool, args)),
  };
}

/**
 * 提取参数的关键部分。
 *
 * 不同工具的关键参数不同：
 *  - bash: 命令的第一个词 + 文件名的 basename
 *  - read/write: 文件名
 *  - grep: 搜索词
 * 这样「读 a.md」「读 b.md」不会被判成重复，但「读 a.md」重复 4 次会。
 */
function looseArgKey(tool, args) {
  if (!args || typeof args !== 'object') return '';

  switch (tool) {
    case 'bash': {
      const cmd = String(args.command || '');
      // 取第一个词 + 出现的文件名
      const firstWord = cmd.trim().split(/\s+/)[0] || '';
      const fileMatch = cmd.match(/[\w\-./]+\.(md|json|txt|docx|py|js|mjs|go)/g) || [];
      return firstWord + '|' + fileMatch.map(basename).sort().join(',');
    }
    case 'read':
    case 'read_file':
    case 'write':
    case 'write_file':
    case 'edit':
      return basename(String(args.path || args.file_path || ''));
    case 'grep':
      return String(args.pattern || args.query || '');
    case 'find':
    case 'ls':
    case 'list_files':
      return basename(String(args.path || args.dir || '.')) + '|' + String(args.pattern || '');
    default:
      // 通用：取所有字符串参数的值拼起来
      return Object.values(args)
        .filter((v) => typeof v === 'string')
        .map((v) => v.slice(0, 40))
        .sort()
        .join('|');
  }
}

/** 去路径前的 basename */
function basename(p) {
  const parts = String(p).split(/[/\\]/);
  return parts[parts.length - 1] || '';
}

/**
 * 稳定序列化：key 排序，保证同样内容总是同样字符串。
 */
function stableStringify(value) {
  if (value === null || value === undefined) return 'null';
  if (typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) {
    return '[' + value.map(stableStringify).join(',') + ']';
  }
  const keys = Object.keys(value).sort();
  return '{' + keys.map((k) => JSON.stringify(k) + ':' + stableStringify(value[k])).join(',') + '}';
}

/** 简易哈希（FNV-1a 32 位，够用且快） */
function hashString(str) {
  let hash = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    hash ^= str.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16);
}

/**
 * 构造「跳出空转」的提示。
 *
 * 分两级：
 *  - level 1（轻）：提醒换个思路
 *  - level 2（重）：明确要求停止当前方式，给出具体替代做法
 */
export function buildStallNudge(reason, level, stage) {
  const lines = [];

  if (level >= 2) {
    lines.push('## ⚠️ 检测到重复操作');
    lines.push('');
    lines.push(`问题：${reason}`);
    lines.push('');
    lines.push('**请立即停止当前做法**，不要再用同样的方式重试。');
    lines.push('');
    lines.push('按以下方式继续：');
    lines.push('1. 先用 `ls` 或 `list_files` 看清工作区里已有什么');
    lines.push(
      stage?.outputFile
        ? `2. 检查产物 \`${stage.outputFile}\` 是否已存在；已有的内容不要重复生成`
        : '2. 检查已有产物，不要重复生成已有内容',
    );
    lines.push('3. 只做还缺的那部分工作');
    lines.push('4. 如果确实无法继续，直接在回复里说明卡在哪里，不要无限重试');
    lines.push('');
    lines.push('**不要**去读软件自身的源码或安装目录，那里没有你要找的东西。');
  } else {
    lines.push('提示：检测到重复操作。');
    lines.push('');
    lines.push(`原因：${reason}`);
    lines.push('');
    lines.push('请换个思路，或先确认已有进展再继续。不要重复同样的调用。');
  }

  return lines.join('\n');
}
