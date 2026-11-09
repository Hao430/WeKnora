export type GuidePlacement = 'right' | 'left' | 'bottom' | 'top'

export interface SpotlightGuideTargetItem {
  selector: string
  /** 洞旁文案键：`${stepI18nPrefix}.${step.key}.items.${itemKey}` */
  itemKey: string
  placement?: GuidePlacement
}

export interface SpotlightGuideStep {
  key: string
  /** 高亮目标的 CSS 选择器；缺省表示居中卡片 */
  target?: string
  /** 多目标同屏蒙版：一次打多个洞，每个洞旁独立文案，不展示分步按钮 */
  targets?: SpotlightGuideTargetItem[]
  placement?: GuidePlacement
  before?: () => void | Promise<void>
  /** 目标不存在时是否跳过该步骤 */
  optional?: boolean
  /** 为 true 时引导用户直接点击高亮区域，不展示「下一步」 */
  interact?: boolean
}
