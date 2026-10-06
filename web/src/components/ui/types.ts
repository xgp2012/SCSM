/**
 * Transport-neutral UI types.
 *
 * These mirror the fuxsto-design prop shapes 1:1 (verified against the
 * installed .d.ts files) but are declared locally so application code does not
 * import types from the library directly. Swapping the library only requires
 * editing this file.
 */

export interface TableColumn {
  key: string
  title: string
  width?: string | number
  align?: 'left' | 'center' | 'right'
  sortable?: boolean | ((a: unknown, b: unknown) => number)
  icon?: unknown
}

export interface UploadFile {
  uid: string
  name: string
  status: 'uploading' | 'done' | 'error'
  size: number
  percent?: number
  url?: string
  raw?: File
  response?: unknown
  error?: Error
}

export interface TabOption {
  label?: string
  value: string | number
  disabled?: boolean
  icon?: unknown
}

export interface SelectOption {
  label: string
  value: string | number
  disabled?: boolean
}

export interface TreeNode {
  label: string
  value: string | number
  key?: string
  disabled?: boolean
  isLeaf?: boolean
  children?: TreeNode[]
}
