import { useCallback, useEffect, useMemo, useState } from 'react';
import { EmptyState, useToast } from '../../../shared/ui';

/**
 * 产物列表页。
 *
 * 为什么 Web 版需要这个页面：
 *   桌面版产物直接落在用户本地磁盘（userData/workspace/），
 *   用户用文件管理器就能找到，不需要列表页。
 *
 *   Web 版产物在云端对象存储，用户必须通过界面才能看到和下载。
 */

interface AssetItem {
  id: string;
  name: string;
  kind: string;
  mimeType: string;
  sizeBytes: number;
  createdAt: string;
  updatedAt: string;
  source: string;
  taskId?: string;
  metadata?: Record<string, unknown>;
}

type FilterKind = 'all' | 'docx' | 'image' | 'markdown' | 'other';

const KIND_LABEL: Record<string, string> = {
  docx: 'Word 文档',
  image: '图片',
  markdown: 'Markdown',
  pdf: 'PDF',
  other: '其他',
};

const FILTERS: { id: FilterKind; label: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'docx', label: 'Word' },
  { id: 'image', label: '图片' },
  { id: 'markdown', label: 'Markdown' },
  { id: 'other', label: '其他' },
];

/** 字节数格式化。 */
function formatSize(bytes: number): string {
  if (!bytes) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** 时间格式化。 */
function formatTime(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const now = Date.now();
  const diff = now - d.getTime();
  if (diff < 60_000) return '刚刚';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return d.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

/** 从文件名推断图标类型（用 SVG 而不是 emoji，避免字体缺字渲染成方块）。 */
function FileIcon({ asset }: { asset: AssetItem }) {
  const common = {
    width: 18,
    height: 18,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.8,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
  };

  if (asset.kind === 'image') {
    return (
      <svg {...common} aria-hidden>
        <rect x="3" y="3" width="18" height="18" rx="2" />
        <circle cx="8.5" cy="8.5" r="1.5" />
        <path d="M21 15l-5-5L5 21" />
      </svg>
    );
  }
  if (asset.name.endsWith('.docx') || asset.name.endsWith('.doc')) {
    return (
      <svg {...common} aria-hidden>
        <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
        <path d="M14 2v6h6" />
        <path d="M8 13h8M8 17h5" />
      </svg>
    );
  }
  if (asset.name.endsWith('.md') || asset.name.endsWith('.txt')) {
    return (
      <svg {...common} aria-hidden>
        <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
        <path d="M14 2v6h6" />
        <path d="M7 15h3l1.5-3L13 18l1.5-3H17" />
      </svg>
    );
  }
  if (asset.name.endsWith('.pdf')) {
    return (
      <svg {...common} aria-hidden>
        <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
        <path d="M14 2v6h6" />
      </svg>
    );
  }
  return (
    <svg {...common} aria-hidden>
      <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
      <path d="M3.3 7L12 12l8.7-5M12 22V12" />
    </svg>
  );
}

function AssetsPage() {
  const [assets, setAssets] = useState<AssetItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState<FilterKind>('all');
  const [keyword, setKeyword] = useState('');
  const [previewUrl, setPreviewUrl] = useState('');
  const [downloading, setDownloading] = useState('');
  const { showToast } = useToast();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      // 用空 kind 拿全部类型
      const result = await window.yibiao.assets.list({} as never);
      const list = (result as { assets?: AssetItem[] })?.assets || [];
      setAssets(list);
    } catch (error) {
      showToast(`加载产物失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    void load();
  }, [load]);

  // 区分为「最终产物」和「过程文件」。
  // manifest.json、tender.md 这类是流程中间文件，不是用户要的产物，
  // 默认隐藏，避免列表被噪音淹没。
  const isIntermediate = (a: AssetItem) =>
    a.name.endsWith('content-manifest.json') ||
    a.name.endsWith('tender.md') ||
    a.name.endsWith('.json');

  const [showIntermediate, setShowIntermediate] = useState(false);

  const filtered = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    return assets.filter((a) => {
      if (!showIntermediate && isIntermediate(a)) return false;
      // 类型过滤
      if (filter !== 'all') {
        const isDocx = a.name.endsWith('.docx') || a.name.endsWith('.doc');
        const isMd = a.name.endsWith('.md');
        const isImage = a.kind === 'image';
        if (filter === 'docx' && !isDocx) return false;
        if (filter === 'image' && !isImage) return false;
        if (filter === 'markdown' && !isMd) return false;
        if (filter === 'other' && (isDocx || isMd || isImage)) return false;
      }
      // 关键词
      if (kw && !a.name.toLowerCase().includes(kw)) return false;
      return true;
    });
  }, [assets, filter, keyword, showIntermediate]);

  // 统计只算「最终产物」，过程文件不计入（否则用户看到的数字虚高）
  const stats = useMemo(() => {
    const finals = assets.filter((a) => !isIntermediate(a));
    return {
      total: finals.length,
      bytes: finals.reduce((sum, a) => sum + (a.sizeBytes || 0), 0),
      images: finals.filter((a) => a.kind === 'image').length,
      intermediates: assets.length - finals.length,
    };
  }, [assets]);

  const handleDownload = async (asset: AssetItem) => {
    setDownloading(asset.id);
    try {
      const url = await window.yibiao.assets.download(asset.id);
      // 用 a[download] 触发浏览器下载，保留原文件名
      const a = document.createElement('a');
      a.href = url;
      a.download = asset.name.split('/').pop() || asset.name;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      showToast('已开始下载', 'success');
    } catch (error) {
      showToast(`下载失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    } finally {
      setDownloading('');
    }
  };

  const handlePreview = async (asset: AssetItem) => {
    try {
      const url = await window.yibiao.assets.download(asset.id);
      setPreviewUrl(url);
    } catch (error) {
      showToast(`预览失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    }
  };

  return (
    <div className="page-shell assets-page">
      <header className="assets-header">
        <div>
          <h1>产物库</h1>
          <p className="assets-subtitle">
            生成的 Word 文档、配图和其他产物都保存在这里
          </p>
        </div>
        <button
          type="button"
          className="secondary-action"
          onClick={() => void load()}
          disabled={loading}
        >
          {loading ? '刷新中…' : '刷新'}
        </button>
      </header>

      <div className="assets-stats">
        <div className="assets-stat">
          <span className="assets-stat-value">{stats.total}</span>
          <span className="assets-stat-label">个产物</span>
        </div>
        <div className="assets-stat">
          <span className="assets-stat-value">{formatSize(stats.bytes)}</span>
          <span className="assets-stat-label">总大小</span>
        </div>
        <div className="assets-stat">
          <span className="assets-stat-value">{stats.images}</span>
          <span className="assets-stat-label">张配图</span>
        </div>
        <div className="assets-stat">
          <span className="assets-stat-value">{stats.intermediates}</span>
          <span className="assets-stat-label">个过程文件</span>
        </div>
      </div>

      <div className="assets-toolbar">
        <div className="assets-filters">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              className={`assets-filter${filter === f.id ? ' is-active' : ''}`}
              onClick={() => setFilter(f.id)}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="assets-toolbar-right">
          <label className="assets-checkbox">
            <input
              type="checkbox"
              checked={showIntermediate}
              onChange={(e) => setShowIntermediate(e.target.checked)}
            />
            显示过程文件
          </label>
          <input
            type="text"
            className="assets-search"
            placeholder="搜索文件名…"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
          />
        </div>
      </div>

      {loading && assets.length === 0 ? (
        <div className="assets-loading">加载中…</div>
      ) : filtered.length === 0 ? (
        <EmptyState
          title={assets.length === 0 ? '还没有产物' : '没有匹配的产物'}
          hint={
            assets.length === 0
              ? '生成技术方案正文后，导出的 Word 文档和配图会出现在这里'
              : '试试换个筛选条件或清空搜索关键词'
          }
        />
      ) : (
        <ul className="assets-list">
          {filtered.map((asset) => (
            <li key={asset.id} className="assets-item">
              <span className="assets-icon" aria-hidden>
                <FileIcon asset={asset} />
              </span>
              <div className="assets-main">
                <div className="assets-name" title={asset.name}>
                  {asset.name.split('/').pop() || asset.name}
                </div>
                <div className="assets-meta">
                  <span>{KIND_LABEL[asset.kind] || asset.kind}</span>
                  <span>·</span>
                  <span>{formatSize(asset.sizeBytes)}</span>
                  <span>·</span>
                  <span>{formatTime(asset.updatedAt || asset.createdAt)}</span>
                </div>
              </div>
              {/* 行操作：图标 + 悬停显示文字。
                  原来每行一个 88px 宽的主色「下载」按钮，
                  一屏八九个蓝块，视觉重量过载，也压过了文件名本身。 */}
              <div className="assets-actions">
                {asset.kind === 'image' ? (
                  <button
                    type="button"
                    className="assets-icon-btn"
                    onClick={() => void handlePreview(asset)}
                    title="预览"
                    aria-label="预览"
                  >
                    <span aria-hidden="true">◫</span>
                  </button>
                ) : null}
                <button
                  type="button"
                  className="assets-icon-btn is-primary"
                  onClick={() => void handleDownload(asset)}
                  disabled={downloading === asset.id}
                  title="下载"
                  aria-label="下载"
                >
                  {downloading === asset.id ? (
                    <span aria-hidden="true">⋯</span>
                  ) : (
                    <span aria-hidden="true">↓</span>
                  )}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {previewUrl ? (
        <div className="assets-preview-mask" onClick={() => setPreviewUrl('')} role="presentation">
          <img src={previewUrl} alt="配图预览" className="assets-preview-image" />
        </div>
      ) : null}
    </div>
  );
}

export default AssetsPage;
