import { createApp, h, reactive, type App } from 'vue';
import AppDialog from './AppDialog.vue';

/**
 * 命令式对话框，API 对齐 fuxsto-design 的 `Dialog`。
 *
 * 背景（M7）：fuxsto-design@1.0.4 的 Dialog **不渲染默认插槽/正文**（正文恒为空），
 * 因此面板改用本自研实现承载确认/提示与表单弹窗，保留一致的调用方式。
 */

export interface ConfirmDialogOptions {
  title?: string;
  content?: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  loading?: boolean;
  showCancel?: boolean;
  onConfirm?: () => void | Promise<void>;
  onCancel?: () => void;
}

/** 命令式弹出一个确认/提示框 */
export function Dialog(options: ConfirmDialogOptions | string): void {
  const opts = typeof options === 'string' ? { content: options } : options;
  const container = document.createElement('div');
  document.body.appendChild(container);

  const state = reactive({
    open: true,
    loading: opts.loading ?? false,
  });

  let app: App | null = null;
  const dispose = (): void => {
    app?.unmount();
    container.remove();
  };

  app = createApp({
    render() {
      return h(AppDialog, {
        open: state.open,
        title: opts.title,
        content: opts.content,
        confirmText: opts.confirmText,
        cancelText: opts.cancelText,
        danger: opts.danger,
        loading: state.loading,
        showCancel: opts.showCancel ?? !!opts.onConfirm,
        'onUpdate:open': (v: boolean) => {
          state.open = v;
          if (!v) setTimeout(dispose, 200);
        },
        onConfirm: () => {
          state.open = false;
          const result = opts.onConfirm?.();
          if (result instanceof Promise) void result.catch(() => undefined);
          setTimeout(dispose, 200);
        },
        onCancel: () => {
          opts.onCancel?.();
        },
      });
    },
  });
  app.mount(container);
}

/** 成功提示（仅确认按钮） */
Dialog.success = (opts: ConfirmDialogOptions | string): void => {
  const o = typeof opts === 'string' ? { content: opts } : opts;
  Dialog({ ...o, showCancel: false, title: o.title ?? '成功' });
};

/** 错误提示（仅确认按钮） */
Dialog.error = (opts: ConfirmDialogOptions | string): void => {
  const o = typeof opts === 'string' ? { content: opts } : opts;
  Dialog({ ...o, showCancel: false, danger: true, title: o.title ?? '错误' });
};

/** 确认框（返回 Promise<boolean>） */
Dialog.confirm = (opts: ConfirmDialogOptions | string): Promise<boolean> => {
  const o = typeof opts === 'string' ? { content: opts } : opts;
  return new Promise((resolve) => {
    Dialog({
      ...o,
      onConfirm: () => {
        void o.onConfirm?.();
        resolve(true);
      },
      onCancel: () => {
        o.onCancel?.();
        resolve(false);
      },
    });
  });
};

export { AppDialog };
