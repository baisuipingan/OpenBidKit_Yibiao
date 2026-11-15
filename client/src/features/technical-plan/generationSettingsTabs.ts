export type GenerationSettingsTab = 'content' | 'existing-plan' | 'knowledge' | 'length' | 'illustration' | 'writing' | 'appearance';

/**
 * 生成设置页签。
 *
 * 文案规范：用行业标准说法，不用口语/方言。
 * 早期版本用的是「写嘛」「长嘛样」这类口语，虽然短，
 * 但在专业工具里显得不严肃，且表意不明确
 * （「怎么写」到底是问策略还是问格式？）。
 *
 * 每个页签配一句 description，悬停时显示，
 * 让用户不点进去也知道这一项配的是什么。
 *
 * 顺序同时决定工具条逐项前进与回退的顺序。
 */
export const generationSettingsTabs: Array<{ id: GenerationSettingsTab; label: string; description: string }> = [
  { id: 'content', label: '生成范围', description: '选择生成完整技术标，还是只写指定章节' },
  { id: 'existing-plan', label: '原方案参考', description: '上传已有方案，AI 参考其结构与表述风格' },
  { id: 'knowledge', label: '知识库引用', description: '勾选企业素材，生成时自动引用相关内容' },
  { id: 'length', label: '字数控制', description: '设定全文和每个小节的目标字数' },
  { id: 'illustration', label: '配图设置', description: '配置是否配图、配图数量和图片类型' },
  { id: 'writing', label: '生成策略', description: '设定写作风格、术语口径和内容补全方式' },
  { id: 'appearance', label: '导出模板', description: '选择 Word 导出的排版模板' },
];

/** 按 id 取页签定义（找不到时返回 undefined）。 */
export function findGenerationSettingsTab(id: GenerationSettingsTab) {
  return generationSettingsTabs.find((tab) => tab.id === id);
}
