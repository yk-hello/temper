// 真实设备清单：屏幕分辨率 + 系统缩放 → 浏览器里的视口（CSS 像素）和缩放比例。
// 高度已扣掉浏览器标签栏、地址栏（约 130 CSS 像素）和任务栏。决策 D34 的验收矩阵以此为准。
export const DEVICES = [
  { id: 'win-1080-125', name: 'Windows 笔记本 1080p · 125%（最常见；用户本人）', w: 1536, h: 730, dpr: 1.25 },
  { id: 'win-1080-150', name: 'Windows 笔记本 1080p · 150%', w: 1280, h: 590, dpr: 1.5 },
  { id: 'win-1080-175', name: 'Windows 小屏笔记本 1080p · 175%', w: 1097, h: 500, dpr: 1.75 },
  { id: 'desk-1080-100', name: '1080p 台式显示器 · 100%', w: 1920, h: 950, dpr: 1 },
  { id: 'desk-2k-100', name: '2K 27 寸 · 100%', w: 2560, h: 1310, dpr: 1 },
  { id: 'desk-4k-150', name: '4K 27 寸 · 150%', w: 2560, h: 1310, dpr: 1.5 },
  { id: 'desk-4k-200', name: '4K · 200%', w: 1920, h: 950, dpr: 2 },
  { id: 'win-1610-150', name: '16:10 笔记本 2560×1600 · 150%', w: 1707, h: 940, dpr: 1.5 },
  { id: 'surface-150', name: '3:2 Surface 2256×1504 · 150%', w: 1504, h: 880, dpr: 1.5 },
  { id: 'mba-13', name: 'MacBook Air 13 寸 · Retina', w: 1470, h: 830, dpr: 2 },
  { id: 'ultrawide', name: '带鱼屏 3440×1440 · 100%', w: 3440, h: 1310, dpr: 1 },
  { id: 'old-1366', name: '老笔记本 1366×768 · 100%', w: 1366, h: 625, dpr: 1 },
  { id: 'win-125-zoom110', name: '1080p 125% + 浏览器放大 110%', w: 1396, h: 664, dpr: 1.375 },
  { id: 'claude-pane', name: 'Claude 应用侧边浏览器窗（用户实测）', w: 618, h: 691, dpr: 1.25 },
  { id: 'iphone', name: 'iPhone 15', w: 393, h: 760, dpr: 3 },
  { id: 'android', name: '安卓手机（Pixel 级）', w: 412, h: 820, dpr: 2.625 },
]
