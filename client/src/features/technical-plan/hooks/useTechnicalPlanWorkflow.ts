import { useEffect, useState } from 'react';
import { DEFAULT_OUTLINE_WORD_CONTROL_OPTIONS } from '../../../shared/types';
import { technicalPlanStorage } from '../services/technicalPlanStorage';
import type { TechnicalPlanState } from '../types';

const initialState: TechnicalPlanState = {
  step: 'document-analysis',
  tenderFile: null,
  tenderFiles: [],
  bidTemplateExists: false,
  originalPlanFile: null,
  projectOverview: '',
  techRequirements: '',
  bidAnalysisMode: 'key',
  bidAnalysisSelectedTaskIds: [],
  bidAnalysisTasks: {},
  bidAnalysisProgress: 0,
  bidSectionMode: 'single',
  bidSections: [],
  bidSectionExtractionStatus: 'idle',
  bidSectionExtractionError: undefined,
  outlineMode: 'standalone-technical',
  outlineExpansionMode: 'ai-complement',
  outlineWordControlOptions: { ...DEFAULT_OUTLINE_WORD_CONTROL_OPTIONS },
  outlineWordControlSnapshot: undefined,
  referenceKnowledgeDocumentIds: [],
  bidSectionExtractionTask: undefined,
  bidAnalysisTask: undefined,
  outlineGenerationTask: undefined,
  outlineAdjustmentTask: undefined,
  globalFactsMode: 'fabricate',
  globalFactsTask: undefined,
  globalFactsAdjustmentTask: undefined,
  globalFacts: [],
  contentGenerationTask: undefined,
  exportTemplateId: '',
  exportTemplateScope: 'ai-only',
  contentGenerationSections: {},
  contentGenerationPlans: {},
  contentGenerationRuntime: undefined,
  outlineData: null,
};

export function useTechnicalPlanWorkflow() {
  const [state, setState] = useState<TechnicalPlanState>(initialState);
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    let mounted = true;

    const loadCache = async () => {
      // 先从后端取权威状态。
      //
      // 桌面版靠本地 JSON 缓存就够了（同一台机器只有一份数据），
      // 但 Web 版必须读服务端 —— 只读本地缓存会导致：
      // 刷新页面后招标文件"消失"，明明库里已经导入了。
      try {
        const remoteState = await window.yibiao?.technicalPlan.loadState();
        if (mounted && remoteState) {
          setState({ ...initialState, ...remoteState });
          setHydrated(true);
          return;
        }
      } catch (error) {
        console.warn('技术方案远端状态读取失败，回退到本地缓存', error);
      }

      // 远端不可用时回退到本地缓存（离线容错）
      try {
        const cachedState = await technicalPlanStorage.load();
        if (mounted && cachedState) {
          setState({ ...initialState, ...cachedState, outlineExpansionMode: cachedState.outlineExpansionMode || 'ai-complement', globalFactsMode: cachedState.globalFactsMode || 'fabricate' });
        }
      } catch (error) {
        console.warn('技术方案缓存读取失败', error);
      } finally {
        if (mounted) {
          setHydrated(true);
        }
      }
    };

    loadCache();

    return () => {
      mounted = false;
    };
  }, []);

  return {
    hydrated,
    state,
    setState,
  };
}
