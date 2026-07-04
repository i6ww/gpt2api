import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link2Off, Pencil, Plus, RefreshCw, Smartphone, Trash2, Upload } from 'lucide-react';
import { type FormEvent, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { cloudPhoneApi } from '../../lib/services';
import type {
  CloudPhoneCreateBody,
  CloudPhoneItem,
  CloudPhoneStatus,
  CloudPhoneUpdateBody,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, fmtMs } from '../pools/_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const STATUS_OPTIONS: { value: '' | CloudPhoneStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'online', label: '在线' },
  { value: 'offline', label: '离线' },
  { value: 'banned', label: '封禁' },
  { value: 'disabled', label: '停用' },
];

function statusBadge(status: string) {
  switch (status) {
    case 'online':
      return { label: '在线', tone: 'bg-success-soft text-success' };
    case 'offline':
      return { label: '离线', tone: 'bg-warn-soft text-warn' };
    case 'banned':
      return { label: '封禁', tone: 'bg-danger-soft text-danger' };
    case 'disabled':
      return { label: '停用', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

export default function CloudPhonePanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | CloudPhoneStatus>('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<CloudPhoneItem | null>(null);
  const [creating, setCreating] = useState(false);
  const [openImport, setOpenImport] = useState(false);
  useEffect(() => setPage(1), [pageSize, status]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, status, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'cloud-phone', 'list', query],
    queryFn: () => cloudPhoneApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'cloud-phone', 'stats'],
    queryFn: () => cloudPhoneApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'cloud-phone'] });

  const create = useMutation({
    mutationFn: (body: CloudPhoneCreateBody) => cloudPhoneApi.create(body),
    onSuccess: () => {
      refresh();
      setCreating(false);
      toast.success('云手机已添加');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: string; body: CloudPhoneUpdateBody }) =>
      cloudPhoneApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const removeOne = useMutation({
    mutationFn: (id: string) => cloudPhoneApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const gopayUnlinkOpenAI = useMutation({
    mutationFn: (id: string) => cloudPhoneApi.gopayUnlinkOpenAI(id, {}),
    onSuccess: () => {
      refresh();
      toast.success(
        '解绑流程已执行完毕。请在云手机 GoPay → 已连接应用 中确认 OpenAI 是否已消失；若仍在可再点一次或人工点「Hapus」',
      );
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: string[]) => cloudPhoneApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 台`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const items = list.data?.list || [];
  const total = list.data?.total || 0;
  const allChecked = items.length > 0 && items.every((it) => selected.has(it.id));
  const indeterminate = !allChecked && items.some((it) => selected.has(it.id));

  function toggleAll() {
    const next = new Set(selected);
    if (allChecked) items.forEach((it) => next.delete(it.id));
    else items.forEach((it) => next.add(it.id));
    setSelected(next);
  }

  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelected(next);
  }

  async function onBatchDelete() {
    if (selected.size === 0) {
      toast.info('请先勾选要删除的云手机');
      return;
    }
    const ok = await confirm({
      title: `确认删除选中的 ${selected.size} 台云手机？`,
      description: '删除后会同步释放绑定的 GoPay 钱包关联（可在编辑里改回去）。该操作不会自动停机 GeeLark。',
      tone: 'danger',
      confirmLabel: '删除',
    });
    if (ok) batchDelete.mutate(Array.from(selected));
  }

  return (
    <div className="space-y-3">
      <StatRow cols={4}>
        <Stat label="总计" value={stats.data?.total ?? 0} />
        <Stat label="在线" value={stats.data?.online ?? 0} tone="text-success" />
        <Stat label="离线" value={stats.data?.offline ?? 0} tone="text-warn" />
        <Stat label="封禁/停用" value={(stats.data?.banned ?? 0) + (stats.data?.disabled ?? 0)} tone="text-danger" />
      </StatRow>

      <Toolbar>
        <input
          type="text"
          className="input input-sm w-44"
          placeholder="搜索 phone_id / 名称"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && setPage(1)}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => setStatus(e.target.value as '' | CloudPhoneStatus)}
        >
          {STATUS_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>

        <ToolbarSpacer />

        <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
          <RefreshCw size={14} />
          刷新
        </button>
        <button className="btn btn-outline btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} />
          批量导入
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={selected.size === 0}
          onClick={onBatchDelete}
        >
          <Trash2 size={14} />
          删除选中 {selected.size > 0 && `(${selected.size})`}
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
          <Plus size={14} />
          新增云手机
        </button>
      </Toolbar>

      <Section
        title={
          <span className="inline-flex items-center gap-1.5">
            <Smartphone size={14} className="text-klein-500" />
            云手机列表
          </span>
        }
      >
        <div className="overflow-x-auto">
          <table className="data-table w-full text-small">
            <thead>
              <tr>
                <th className="w-8">
                  <input
                    type="checkbox"
                    checked={allChecked}
                    ref={(el) => {
                      if (el) el.indeterminate = indeterminate;
                    }}
                    onChange={toggleAll}
                  />
                </th>
                <th>Phone ID</th>
                <th>名称</th>
                <th>状态</th>
                <th>Token</th>
                <th>WhatsApp / GoPay 手机号</th>
                <th>模式</th>
                <th>上次检测</th>
                <th className="w-32 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={9} className="py-6 text-center text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={9} className="py-6 text-center text-text-tertiary">
                    暂无云手机，先添加 GeeLark phone_id + Bearer Token
                  </td>
                </tr>
              )}
              {items.map((it) => {
                const sb = statusBadge(it.status);
                return (
                  <tr key={it.id}>
                    <td>
                      <input
                        type="checkbox"
                        checked={selected.has(it.id)}
                        onChange={() => toggleOne(it.id)}
                      />
                    </td>
                    <td className="font-mono text-tiny">{it.id}</td>
                    <td>{it.name || <span className="text-text-tertiary">—</span>}</td>
                    <td>
                      <Badge label={sb.label} tone={sb.tone} />
                    </td>
                    <td>
                      <FlagPill ok={it.has_gl_token} label={it.has_gl_token ? '已设置' : '未设置'} />
                    </td>
                    <td>
                      {it.phone_number ? (
                        <code className="text-tiny">
                          +{it.country_code} {it.phone_masked || it.phone_number}
                        </code>
                      ) : (
                        <span className="text-warn">未设置（钱包不可用）</span>
                      )}
                    </td>
                    <td>
                      <span className="text-tiny text-text-tertiary">
                        {it.prefer_api ? 'API 优先' : 'ADB 优先'}
                      </span>
                    </td>
                    <td className="text-tiny text-text-tertiary">{fmtMs(it.last_check_at)}</td>
                    <td className="text-right">
                      <button
                        type="button"
                        className="btn btn-ghost btn-xs"
                        disabled={gopayUnlinkOpenAI.isPending}
                        title="自动操作云手机移除 GoPay 已连接应用中的 OpenAI"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '在云手机执行 GoPay 解绑 OpenAI？',
                            description:
                              '将经 GeeLark 在云手机内模拟点击（约 1～3 分钟）。' +
                              '请保持云手机在线，默认会尝试打开 Gojek 主应用（com.gojek.app）。' +
                              '完成后务必在「Aplikasi yang terhubung」里确认 OpenAI 已消失。',
                            tone: 'warning',
                            confirmLabel: '开始解绑',
                          });
                          if (ok) gopayUnlinkOpenAI.mutate(it.id);
                        }}
                      >
                        <Link2Off size={12} />
                        解绑 OpenAI
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        onClick={() => setEditing(it)}
                      >
                        <Pencil size={12} />
                        编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: `删除云手机 ${it.name || it.id}？`,
                            description: '不会停机 GeeLark，仅从池里软删除。',
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) removeOne.mutate(it.id);
                        }}
                      >
                        <Trash2 size={12} />
                        删除
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>

        <div className="mt-3">
          <Pager
            total={total}
            page={page}
            pageSize={pageSize}
            onChange={setPage}
            onPageSizeChange={setPageSize}
            sizeOptions={sizeOptions}
          />
        </div>
      </Section>

      {(creating || editing) && (
        <CloudPhoneEditDialog
          item={editing}
          busy={create.isPending || update.isPending}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSubmit={(body) => {
            if (editing) {
              update.mutate({ id: editing.id, body });
            } else {
              create.mutate(body as CloudPhoneCreateBody);
            }
          }}
        />
      )}

      {openImport && (
        <CloudPhoneImportDialog
          onClose={() => setOpenImport(false)}
          onDone={() => {
            setOpenImport(false);
            refresh();
          }}
        />
      )}

      {confirmDialog}
    </div>
  );
}

// ─── 编辑/新增弹窗 ───

function CloudPhoneEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: CloudPhoneItem | null;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: CloudPhoneCreateBody | CloudPhoneUpdateBody) => void;
}) {
  const isEdit = !!item;
  const [id, setId] = useState(item?.id || '');
  const [name, setName] = useState(item?.name || '');
  const [glToken, setGlToken] = useState('');
  const [adbAddr, setAdbAddr] = useState(item?.adb_addr || '');
  const [preferAPI, setPreferAPI] = useState<0 | 1>((item?.prefer_api as 0 | 1) ?? 1);
  const [countryCode, setCountryCode] = useState(item?.country_code || '62');
  const [phoneNumber, setPhoneNumber] = useState(item?.phone_number || '');
  const [status, setStatus] = useState<CloudPhoneStatus>((item?.status as CloudPhoneStatus) || 'online');
  const [remark, setRemark] = useState(item?.remark || '');

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (isEdit) {
      const body: CloudPhoneUpdateBody = {
        name: name || undefined,
        gl_token: glToken || undefined,
        adb_addr: adbAddr || undefined,
        prefer_api: preferAPI,
        country_code: countryCode || undefined,
        phone_number: phoneNumber || undefined,
        status,
        remark: remark || undefined,
      };
      onSubmit(body);
    } else {
      if (!id.trim()) {
        toast.error('phone_id 不能为空');
        return;
      }
      if (!glToken.trim()) {
        toast.error('GeeLark Token 不能为空');
        return;
      }
      const body: CloudPhoneCreateBody = {
        id: id.trim(),
        name: name || undefined,
        gl_token: glToken.trim(),
        adb_addr: adbAddr || undefined,
        prefer_api: preferAPI,
        country_code: countryCode || undefined,
        phone_number: phoneNumber || undefined,
        remark: remark || undefined,
      };
      onSubmit(body);
    }
  }

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <form
        className="dialog-surface relative w-full max-w-xl space-y-3 p-4 sm:p-6"
        onSubmit={handleSubmit}
      >
        <header>
          <h3 className="text-h4 text-text-primary">{isEdit ? '编辑云手机' : '新增云手机'}</h3>
          <p className="mt-1 text-tiny text-text-tertiary">
            phone_id 是 GeeLark 后台的设备 ID；token 是子账号的 OpenAPI Bearer。
          </p>
        </header>

        <div className="grid grid-cols-2 gap-3">
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">Phone ID *</span>
            <input
              className="input w-full"
              value={id}
              onChange={(e) => setId(e.target.value)}
              disabled={isEdit}
              placeholder="例如 g_1234567890"
            />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">名称（备注）</span>
            <input className="input w-full" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="col-span-2 space-y-1">
            <span className="text-tiny text-text-secondary">
              GeeLark Token{!isEdit && ' *'}
              {isEdit && (
                <span className="ml-2 text-text-tertiary">（留空则不动）</span>
              )}
            </span>
            <input
              className="input w-full font-mono text-tiny"
              type="password"
              value={glToken}
              onChange={(e) => setGlToken(e.target.value)}
              placeholder="Bearer Token"
              autoComplete="off"
            />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">ADB 地址（可选）</span>
            <input
              className="input w-full font-mono text-tiny"
              value={adbAddr}
              onChange={(e) => setAdbAddr(e.target.value)}
              placeholder="ip:port:pwd"
            />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">连接优先模式</span>
            <select
              className="select w-full"
              value={preferAPI}
              onChange={(e) => setPreferAPI(Number(e.target.value) as 0 | 1)}
            >
              <option value={1}>API 优先（服务器部署推荐）</option>
              <option value={0}>ADB 优先（本机直连）</option>
            </select>
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">国家代码</span>
            <input
              className="input w-full"
              value={countryCode}
              onChange={(e) => setCountryCode(e.target.value)}
              placeholder="62"
            />
          </label>
          <label className="col-span-2 space-y-1">
            <span className="text-tiny text-text-secondary">
              WhatsApp / GoPay 手机号 <span className="text-text-tertiary">（不带 + 不带国家码）</span>
            </span>
            <input
              className="input w-full font-mono text-tiny"
              value={phoneNumber}
              onChange={(e) => setPhoneNumber(e.target.value)}
              placeholder="838xxxxxxxx"
            />
          </label>
          {isEdit && (
            <label className="space-y-1">
              <span className="text-tiny text-text-secondary">状态</span>
              <select
                className="select w-full"
                value={status}
                onChange={(e) => setStatus(e.target.value as CloudPhoneStatus)}
              >
                <option value="online">在线</option>
                <option value="offline">离线</option>
                <option value="banned">封禁</option>
                <option value="disabled">停用</option>
              </select>
            </label>
          )}
          <label className="col-span-2 space-y-1">
            <span className="text-tiny text-text-secondary">备注</span>
            <input className="input w-full" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </label>
        </div>

        <div className="flex justify-end gap-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </div>
  );
}

// ─── 批量导入弹窗 ───

function CloudPhoneImportDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [text, setText] = useState('');

  const importMu = useMutation({
    mutationFn: () => cloudPhoneApi.import({ text }),
    onSuccess: (r) => {
      toast.success(`新增 ${r.imported} / 更新 ${r.updated} / 跳过 ${r.skipped}`);
      if (r.errors && r.errors.length > 0) {
        toast.info(`部分失败：${r.errors.slice(0, 3).join('；')}`);
      }
      onDone();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <ImportDialogShell
      title="批量导入云手机"
      description={
        <span>
          一行一台，分隔符 <code>|</code>：
          <code>phone_id|gl_token[|adb_addr][|name][|country_code][|phone_number][|remark]</code>
          <br />
          country_code 默认 62（印尼），phone_number 不带 + 不带国家码。
        </span>
      }
      busy={importMu.isPending}
      onClose={onClose}
      onConfirm={() => {
        if (!text.trim()) {
          toast.error('请粘贴要导入的内容');
          return;
        }
        importMu.mutate();
      }}
    >
      <textarea
        className="textarea w-full font-mono text-tiny"
        rows={10}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={`g_1234567890|eyJhbGciOiJI...|192.168.1.10:5555:pwd|主力机|62|838xxxxxxxx|备注\ng_2233445566|eyJhbGciOiJI...`}
      />
    </ImportDialogShell>
  );
}
