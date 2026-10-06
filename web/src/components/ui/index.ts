/**
 * ui/ — the thin adapter layer required by plan §4.4 point 3.
 *
 * RULE: application code imports UI primitives from `@/components/ui` (or
 * `@/components/ui/cn`) and never from `fuxsto-design/*` directly. This module
 * is the single swap point: today it re-exports fuxsto-design@1.0.5 (all
 * subpaths verified to resolve); tomorrow it can forward to a patched build, a
 * local implementation, or a different library without touching a page.
 *
 * Components that the library does NOT provide (there is no terminal and no
 * chart component — plan §4.4) live in this folder as local implementations:
 *   · console/XtermTerminal.vue  → xterm.js
 *   · monitor/MetricChart.vue    → uplot
 */

/* ---- re-exported from fuxsto-design (verified subpaths) ---- */
export { default as UiButton } from 'fuxsto-design/button'
export { default as UiButtonGroup } from 'fuxsto-design/button-group'
export { default as UiCard } from 'fuxsto-design/card'
export { default as UiBadge } from 'fuxsto-design/badge'
export { default as UiTag } from 'fuxsto-design/tag'
export { default as UiTabs } from 'fuxsto-design/tabs'
export { default as UiMenu } from 'fuxsto-design/menu'
export { default as UiBreadcrumb } from 'fuxsto-design/breadcrumb'
export { default as UiBreadcrumbItem } from 'fuxsto-design/breadcrumb-item'
export { default as UiTable } from 'fuxsto-design/table'
export { default as UiPagination } from 'fuxsto-design/pagination'
export { default as UiProgress } from 'fuxsto-design/progress'
export { default as UiStatistic } from 'fuxsto-design/statistic'
export { default as UiSegmented } from 'fuxsto-design/segmented'
export { default as UiTimeline } from 'fuxsto-design/timeline'
export { default as UiTimelineItem } from 'fuxsto-design/timeline-item'
export { default as UiDivider } from 'fuxsto-design/divider'
export { default as UiScrollArea } from 'fuxsto-design/scroll-area'
export { default as UiVirtualList } from 'fuxsto-design/virtual-list'
export { default as UiTree } from 'fuxsto-design/tree'
export { default as UiForm } from 'fuxsto-design/form'
export { default as UiFormItem } from 'fuxsto-design/form-item'
export { default as UiInput } from 'fuxsto-design/input'
export { default as UiInputNumber } from 'fuxsto-design/input-number'
export { default as UiTextarea } from 'fuxsto-design/textarea'
export { default as UiSelect } from 'fuxsto-design/select'
export { default as UiSwitch } from 'fuxsto-design/switch'
export { default as UiCheckbox } from 'fuxsto-design/checkbox'
export { default as UiRadio } from 'fuxsto-design/radio'
export { default as UiRadioGroup } from 'fuxsto-design/radio-group'
export { default as UiSlider } from 'fuxsto-design/slider'
export { default as UiUpload } from 'fuxsto-design/upload'
export { default as UiDialog } from 'fuxsto-design/dialog'
export { default as UiDrawer } from 'fuxsto-design/drawer'
export { default as UiTooltip } from 'fuxsto-design/tooltip'
export { default as UiPopconfirm } from 'fuxsto-design/popconfirm'
export { default as UiEmpty } from 'fuxsto-design/empty'
export { default as UiLoading } from 'fuxsto-design/loading'
export { default as UiSkeleton } from 'fuxsto-design/skeleton'
export { default as UiAlert } from 'fuxsto-design/alert'
export { default as UiResult } from 'fuxsto-design/result'
export { default as UiList } from 'fuxsto-design/list'
export { default as UiListItem } from 'fuxsto-design/list-item'

/* ---- imperative helpers (functions, not components) ---- */
export { Message } from 'fuxsto-design/message'
export { Notification } from 'fuxsto-design/notification'
export { Dialog } from 'fuxsto-design/dialog'

/* ---- shared utilities ---- */
export { cn } from './cn'

/* ---- local UI shims (things the library does not ship) ---- */
export { default as UiKeyValue } from './UiKeyValue.vue'
export { default as UiSectionNote } from './UiSectionNote.vue'
export { default as UiStatTile } from './UiStatTile.vue'
export { default as UiNotAvailable } from './UiNotAvailable.vue'

/* ---- local type mirrors (keep third-party types out of app code) ---- */
export type { TableColumn } from './types'
export type { UploadFile } from './types'
export type { TabOption } from './types'
export type { SelectOption } from './types'
export type { TreeNode } from './types'
