// 术语表（3.26）：一句白话，说清楚是什么、对你有什么影响。
export interface Term { zh: [string, string]; en: [string, string] }

export const GLOSSARY: Term[] = [
  { zh: ['标记价', '算浮动盈亏、触发强平用的价，由交易所综合多个来源算出，不容易被一笔插针带跑。'], en: ['Mark price', 'The price used for unrealized PnL and liquidation. The exchange blends several sources, so a single spike cannot move it far.'] },
  { zh: ['资金费', '永续合约每 8 小时多空双方互相付一次钱。费率为正，多头付给空头；为负则反过来。'], en: ['Funding', 'Every 8 hours, longs and shorts pay each other. When the rate is positive, longs pay shorts; when negative, the reverse.'] },
  { zh: ['强平', '亏到保证金不够维持仓位时，系统按市价平掉你的仓位，并收一笔强平费。'], en: ['Liquidation', 'When losses leave too little margin, the system closes your position at market and charges a liquidation fee.'] },
  { zh: ['全仓', '账户里所有可用资金一起给仓位当保证金。不容易爆，但爆了亏的是整个账户。'], en: ['Cross margin', 'All available funds back your positions. Harder to liquidate, but a liquidation hits the whole account.'] },
  { zh: ['逐仓', '每个仓位只用分给它的保证金。亏完这部分就强平，不影响账户里其余的钱。'], en: ['Isolated margin', 'Each position uses only its own margin. Losing it liquidates that position and leaves the rest of the account alone.'] },
  { zh: ['杠杆', '仓位价值是保证金的几倍。杠杆越高，离强平价越近。'], en: ['Leverage', 'How many times larger the position is than its margin. Higher leverage puts liquidation closer.'] },
  { zh: ['保证金率', '维持保证金 ÷ 保证金余额。到 100% 就强平。'], en: ['Margin ratio', 'Maintenance margin ÷ margin balance. At 100% the position is liquidated.'] },
  { zh: ['挂单', '把单子放在盘口上等别人来成交，手续费低（0.02%），但可能不成交。'], en: ['Maker order', 'Rests in the book waiting for a counterparty. Lower fee (0.02%), but it may not fill.'] },
  { zh: ['吃单', '直接和盘口上已有的单成交，马上成交，手续费高（0.05%）。'], en: ['Taker order', 'Trades against resting orders right away. Fills immediately at a higher fee (0.05%).'] },
  { zh: ['滑点', '实际成交价和你想要的价之间的差。盘口越薄、单子越大，滑点越大。'], en: ['Slippage', 'The gap between the price you wanted and the fill. Thinner books and larger orders mean more slippage.'] },
  { zh: ['只减仓', '这张单只会减少现有仓位，不会开出反方向的新仓。'], en: ['Reduce only', 'The order can only shrink your position; it never opens one in the other direction.'] },
  { zh: ['止盈止损', '价格到了设定的位置就自动平仓，一边触发，另一边自动撤掉。'], en: ['Take profit / stop loss', 'Closes the position when price reaches your level. When one triggers, the other is cancelled.'] },
  { zh: ['条件单', '价格到了才下的单，到之前不占保证金。'], en: ['Conditional order', 'An order placed only when price reaches a level. It uses no margin until then.'] },
  { zh: ['目标仓位', 'AI 每次判断发出的是"应该持有多少"（占净值的比例），不是买卖动作。系统按差额下单。'], en: ['Target position', 'Each AI decision says how much to hold, as a share of net value, not buy or sell. Orders cover the difference.'] },
  { zh: ['模型组合', 'AI 策略自己的模拟账户，初始 10,000 USDT，按真实盘口成交。战绩就是它的净值。'], en: ['Model portfolio', 'The AI strategy\'s own paper account, starting at 10,000 USDT and filled against real books. Its record is this account\'s value.'] },
  { zh: ['回撤', '净值从高点往下跌了多少。最大回撤是历史上跌得最深的一次。'], en: ['Drawdown', 'How far net value has fallen from a peak. Max drawdown is the deepest such fall.'] },
  { zh: ['夏普', '收益相对波动的比值，越高越稳。这里按每天的收益算，再换算成一年。'], en: ['Sharpe ratio', 'Return relative to volatility; higher is steadier. Calculated from daily returns and annualized.'] },
  { zh: ['胜率', '净收益大于 0 的交易占比。胜率高不等于赚钱，还要看赚亏的比例。'], en: ['Win rate', 'Share of trades with net profit. A high win rate does not guarantee profit; size of wins and losses matters too.'] },
  { zh: ['信心', 'AI 每次决策写的把握（0–100%）。只用来展示和校准，不影响仓位大小。'], en: ['Confidence', 'How sure the AI says it is (0–100%). Used for display and checking only; it does not change position size.'] },
  { zh: ['跟随滑点', '你的成交价和 AI 模型组合成交价的差。'], en: ['Follow slippage', 'The gap between your fill price and the AI model portfolio\'s.'] },
  { zh: ['最大亏损', '你给跟随设的亏损上限。亏到这里自动平仓并停止；极端行情可能多亏一些。'], en: ['Max loss', 'The loss limit you set for following. At this level, positions close and following stops; extreme moves can lose somewhat more.'] },
]
