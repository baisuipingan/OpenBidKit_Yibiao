/**
 * 使用行为上报（已停用）。
 *
 * 原来这里把设备 ID、页面访问、配置组合、点击行为上报到第三方服务器。
 * 现在产品定位是内部标书工作台，不需要这些运营数据，
 * 而且采集设备指纹本身也有隐私风险。
 *
 * **保留同名导出而不是删除调用点**：调用散布在 10+ 个页面里，
 * 逐个删价值低，还会让后续从上游同步代码时冲突。
 * 改为 no-op 后调用点一行都不用动。
 */

/** 应用启动：不再上报 */
export function trackAppOpen(): void {}

/** 页面访问：不再上报 */
export function trackPageView(_page: string): void {}

/** 更新日志打开：不再上报 */
export function trackChangelogOpen(): void {}

/** 配置使用情况：不再上报 */
export function trackConfigUsage(_payload?: unknown, _config?: unknown): void {}

/** 资源点击：不再上报 */
export function trackResourceClick(_resourceKey: string): void {}

/** 广告点击：不再上报 */
export function trackAdClick(_adKey: string): void {}

/** 公告查看：不再上报 */
export function trackNoticeBoardOpen(): void {}
