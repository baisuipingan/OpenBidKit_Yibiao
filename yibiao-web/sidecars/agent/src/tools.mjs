/**
 * 业务工具注册机制。
 *
 * 设计目标：让 Go 侧能注册任意工具，sidecar 保持通用，
 * 不硬编码任何标书业务逻辑。
 *
 * 工作方式：
 *   Go 创建会话时通过 customTools 传入工具声明：
 *   [
 *     {
 *       name: "generate_image",
 *       description: "生成一张配图",
 *       parameters: { type: "object", properties: {...}, required: [...] },
 *       callbackUrl: "http://127.0.0.1:8080/api/agent-tools/generate_image"
 *     }
 *   ]
 *
 *   sidecar 把它包成 Pi 的 tool：Agent 调用时，
 *   sidecar POST 到 callbackUrl，把返回结果转成 tool result。
 *
 * 为什么这样设计（对比桌面版）：
 *   桌面版的业务工具直接 require 业务服务，Agent 层和业务层强耦合。
 *   Web 版通过 HTTP 回调解耦：
 *     - 业务逻辑全在 Go（能用 Go 的类型和测试）
 *     - sidecar 只做转发，换掉 Pi SDK 时工具层不用重写
 */

import { Type } from 'typebox';

/**
 * 把 JSON Schema 转成 TypeBox schema。
 *
 * Agent 工具的 parameters 是 JSON Schema 格式（OpenAI 兼容），
 * Pi 需要 TypeBox。这里做有限映射，覆盖标书场景需要的类型。
 */
function jsonSchemaToTypeBox(schema, T) {
  if (!schema || typeof schema !== 'object') {
    return T.Any();
  }

  const type = schema.type;

  if (type === 'object' || schema.properties) {
    const props = schema.properties || {};
    const required = new Set(schema.required || []);
    const shape = {};

    for (const [key, value] of Object.entries(props)) {
      const fieldSchema = jsonSchemaToTypeBox(value, T);
      shape[key] = required.has(key) ? fieldSchema : T.Optional(fieldSchema);
    }

    // 允许额外字段：Agent 可能传格式要求的补充信息
    return T.Object(shape, { additionalProperties: true });
  }

  if (type === 'array') {
    const items = jsonSchemaToTypeBox(schema.items || {}, T);
    return T.Array(items);
  }

  if (type === 'string') {
    if (Array.isArray(schema.enum) && schema.enum.length) {
      return T.Union(schema.enum.map((v) => T.Literal(String(v))));
    }
    return T.String();
  }

  if (type === 'number' || type === 'integer') {
    return T.Number();
  }

  if (type === 'boolean') {
    return T.Boolean();
  }

  return T.Any();
}

/**
 * 创建 HTTP 回调工具。
 *
 * @param {object} decl 工具声明 { name, description, parameters, callbackUrl }
 * @param {object} ctx  上下文 { Type, internalToken, fetchImpl }
 * @returns {object} Pi tool definition（未包装）
 */
export function createCallbackTool(decl, ctx) {
  const { Type: T, internalToken = '', timeoutMs = 120000, sessionId = '', taskId = '' } = ctx;

  return {
    name: decl.name,
    description: decl.description || '',
    parameters: jsonSchemaToTypeBox(decl.parameters, T),

    // Pi 的 execute 签名是 (toolCallId, params)，不是 (args)！
    // 桌面版 piJsonValidationTool 用的就是 (_toolCallId, params)。
    execute: async (_toolCallId, params) => {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);

      try {
        const headers = { 'Content-Type': 'application/json' };
        if (internalToken) headers.Authorization = `Bearer ${internalToken}`;

        // 参数归一化：模型有时把 params 序列化成字符串传，
        // 这里统一成对象，避免 Go 侧反序列化失败。
        const normalizedArgs = normalizeArgs(params);

        const resp = await fetch(decl.callbackUrl, {
          method: 'POST',
          headers,
          // 必须带 sessionId / taskId：
          // Go 侧据此反查租户、项目、工作区，防止越权。
          body: JSON.stringify({
            args: normalizedArgs,
            sessionId: ctx.sessionId || '',
            taskId: ctx.taskId || '',
          }),
          signal: controller.signal,
        });

        const text = await resp.text();
        let payload;
        try {
          payload = JSON.parse(text);
        } catch {
          // 非 JSON 响应：当作纯文本结果
          return {
            content: [{ type: 'text', text: text.slice(0, 4000) }],
            isError: !resp.ok,
          };
        }

        if (!resp.ok || payload?.ok === false) {
          const msg = payload?.error?.message || `工具执行失败（HTTP ${resp.status}）`;
          return {
            content: [{ type: 'text', text: msg }],
            isError: true,
          };
        }

        // 结果格式：{ ok: true, data: { text?, images?, files?, ... } }
        const data = payload.data ?? payload;

        return {
          content: buildContent(data),
          details: data,
        };
      } catch (e) {
        const msg = e?.name === 'AbortError'
          ? `工具调用超时（${Math.round(timeoutMs / 1000)} 秒）`
          : `工具调用失败：${e?.message || e}`;
        return {
          content: [{ type: 'text', text: msg }],
          isError: true,
        };
      } finally {
        clearTimeout(timer);
      }
    },
  };
}

/**
 * 归一化工具参数。
 *
 * 模型输出习惯差异：有时 args 是对象，有时是「对象的 JSON 字符串」。
 * 统一成对象，避免两端都要容错。
 */
function normalizeArgs(args) {
  if (args === null || args === undefined) return {};

  if (typeof args === 'string') {
    const trimmed = args.trim();
    if (!trimmed) return {};
    if (trimmed[0] === '{' || trimmed[0] === '[') {
      try {
        return JSON.parse(trimmed);
      } catch {
        return {};
      }
    }
    return {};
  }

  if (typeof args === 'object') return args;

  return {};
}

/**
 * 把 Go 返回的 data 转成 Pi 的 content 数组。
 *
 * 支持的形式：
 *   { text: "..." }                    纯文本
 *   { images: [{ path, alt }] }        图片引用（Agent 可在后文引用）
 *   { files: [{ path, description }] }  文件引用
 *   { text, images, files }            组合
 *   "字符串"                            直接文本
 */
function buildContent(data) {
  if (data === null || data === undefined) {
    return [{ type: 'text', text: '（无返回内容）' }];
  }

  if (typeof data === 'string') {
    return [{ type: 'text', text: data }];
  }

  const parts = [];

  if (data.text) {
    parts.push({ type: 'text', text: String(data.text) });
  }

  if (Array.isArray(data.images) && data.images.length) {
    const lines = data.images.map((img, i) => {
      if (typeof img === 'string') return `${i + 1}. ${img}`;
      const alt = img.alt ? `（${img.alt}）` : '';
      return `${i + 1}. ${img.path}${alt}`;
    });
    parts.push({
      type: 'text',
      text: `已生成 ${data.images.length} 张图片：\n${lines.join('\n')}`,
    });
  }

  if (Array.isArray(data.files) && data.files.length) {
    const lines = data.files.map((f, i) => {
      if (typeof f === 'string') return `${i + 1}. ${f}`;
      const desc = f.description ? ` - ${f.description}` : '';
      return `${i + 1}. ${f.path}${desc}`;
    });
    parts.push({
      type: 'text',
      text: `涉及 ${data.files.length} 个文件：\n${lines.join('\n')}`,
    });
  }

  if (!parts.length) {
    // 兜底：序列化整个对象，让 Agent 能看到返回内容
    parts.push({ type: 'text', text: JSON.stringify(data).slice(0, 4000) });
  }

  return parts;
}

/**
 * 批量创建回调工具。
 *
 * @param {Array} decls 工具声明数组
 * @param {object} ctx  上下文
 * @returns {Array} Pi tool definitions
 */
export function createCallbackTools(decls, ctx) {
  if (!Array.isArray(decls) || !decls.length) return [];

  const out = [];
  for (const decl of decls) {
    if (!decl?.name || !decl?.callbackUrl) {
      console.warn('[tools] 跳过无效工具声明:', decl?.name || '(无名)');
      continue;
    }
    try {
      out.push(createCallbackTool(decl, ctx));
    } catch (e) {
      console.error(`[tools] 创建工具 ${decl.name} 失败:`, e?.message);
    }
  }
  return out;
}
