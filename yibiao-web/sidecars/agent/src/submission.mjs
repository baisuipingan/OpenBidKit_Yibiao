/**
 * 提交策略：决定 Agent 提交的产物是接受、继续修复、还是停止。
 *
 * 移植自桌面版 client/electron/services/pi/piSubmissionPolicy.cjs，
 * 逻辑一字未改——这是标书生成质量的核心保障，不能动。
 *
 * 核心思想（这是标书垂类 Agent 的关键设计）：
 *   不按固定轮数限制，而按「相邻两轮的进展」判断：
 *   - 阻塞问题减少，或质量问题减少 → 有进展，继续修复
 *   - 连续 2 轮没进展 → 换策略（stalled=1）→ 再不行则降到最低目标（stalled=2）
 *   - 最低目标那轮还有阻塞问题 → 停止，不无限重试
 *
 * 为什么这样设计：标书正文动辄几十万字，固定轮数要么浪费 token，
 * 要么在长文档上不够用。按进展判断能做到「有效就继续，无效就收手」。
 */

/**
 * 创建一个阶段内的提交策略实例。
 * 每个业务阶段独立维护，阶段切换时重建。
 */
export function createSubmissionPolicy() {
  let previous = null;
  let stalled = 0;
  let repairCount = 0;
  let minimumAttempted = false;

  return {
    get attempts() {
      return repairCount;
    },

    /**
     * 检查一次提交。
     *
     * @param {{ issues: Array<{severity: 'blocking'|'quality', message: string}>, progress?: number, minimumGoal?: string }} report
     * @returns {{ action: 'accept'|'repair'|'stop', mode?: 'normal'|'change-strategy'|'minimum', attempt?: number }}
     */
    inspect(report) {
      const blocking = report.issues.filter((i) => i.severity === 'blocking');

      // 已经走过最低目标那轮：只看阻塞问题
      if (minimumAttempted) {
        return { action: blocking.length ? 'stop' : 'accept' };
      }
      // 没有任何问题：接受
      if (!report.issues.length) {
        return { action: 'accept' };
      }

      // 用 progress 表示剩余质量问题数（业务可显式提供，否则数据组数）
      const current = {
        blocking: blocking.length,
        remaining: report.progress ?? report.issues.filter((i) => i.severity === 'quality').length,
      };

      if (previous) {
        const improved =
          current.blocking < previous.blocking ||
          (current.blocking === 0 && previous.blocking === 0 && current.remaining < previous.remaining);
        stalled = improved ? 0 : stalled + 1;
      }
      previous = current;

      // 连续 2 轮没进展且已无阻塞问题：接受当前质量，不纠缠
      if (stalled >= 2 && !blocking.length) {
        return { action: 'accept' };
      }

      // 没进展才升级策略：0 次 → normal，1 次 → 换方法，2 次 → 最低目标
      const mode = stalled >= 2 ? 'minimum' : stalled === 1 ? 'change-strategy' : 'normal';
      if (mode === 'minimum') minimumAttempted = true;

      repairCount += 1;
      return { action: 'repair', mode, attempt: repairCount };
    },
  };
}

/**
 * 构造修复提示词。
 *
 * 业务提示由调用方提供（补文件路径、工具说明），
 * 这里统一决定「换方法」和「最后一轮最低目标」的措辞。
 */
export function buildSubmissionRepairPrompt(report, decision, businessPrompt = '') {
  const issues =
    decision.mode === 'minimum'
      ? report.issues.filter((i) => i.severity === 'blocking')
      : report.issues;

  const messages = issues.map((issue, index) => `${index + 1}. ${issue.message}`).join('\n');

  const instruction =
    decision.mode === 'minimum'
      ? [
          '连续两次修复未取得进展。现在进行最后一轮最低目标修复。',
          `最低完成目标：${report.minimumGoal || '解决下列阻塞问题，使当前产物能被后续流程正常读取和使用。'}`,
          '本轮只要求消除上述阻塞问题，暂不要求消除一致性、表格、字数等质量瑕疵；此前业务提示中的质量完成条件本轮不再作为验收条件。完成最低目标后重新提交；如果仍有阻塞问题，任务将停止。',
        ].join('\n')
      : decision.mode === 'change-strategy'
        ? '上一轮修复未取得进展。先核对最新产物并定位原方法未解决问题的原因，再更换具体处理方法；不要重复相同操作。可使用当前可用工具和命令验证新的处理方法，修复后重新提交。'
        : '保留已完成成果，根据当前问题修复后重新提交。';

  return [businessPrompt, `本轮待修复问题：\n${messages}`, `这是本阶段第 ${decision.attempt} 次提交修复。`, instruction]
    .filter(Boolean)
    .join('\n\n');
}

/**
 * 执行异常时的重试提示。
 *
 * 与提交修复分开：执行异常消耗 maxRetries 预算，
 * 提交修复不设固定上限（由策略决定何时停）。
 */
export function buildExecutionRetryPrompt(outputFile, error, attempt, maxRetries) {
  return [
    `上一轮执行出现异常（第 ${attempt}/${maxRetries} 次重试）：`,
    String(error?.message || error).slice(0, 800),
    '',
    '请检查当前工作区状态，修正问题后继续完成任务。',
    outputFile ? `最终产物仍需写入：${outputFile}` : '',
  ]
    .filter(Boolean)
    .join('\n');
}
