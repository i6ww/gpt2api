import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Megaphone, Pencil, Plus, RefreshCw, Trash2, X } from 'lucide-react';
import { useState } from 'react';

import { ApiError } from '../../lib/api';
import { fmtTime } from '../../lib/format';
import { announcementsApi } from '../../lib/services';
import type { Announcement, AnnouncementCreateReq, AnnouncementLevel } from '../../lib/types';
import { toast } from '../../stores/toast';
import { PageHeader, PageShell, Section } from '../../components/layout/PageShell';

const LEVEL_LABELS: Record<AnnouncementLevel, string> = {
  info: 'Info（蓝/灰）',
  success: '成功（绿）',
  warning: '警告（橙）',
  danger: '紧急（红）',
};

const LEVEL_BADGE: Record<AnnouncementLevel, string> = {
  info: 'badge badge-outline',
  success: 'badge badge-success',
  warning: 'badge badge-warning',
  danger: 'badge badge-danger',
};

function blankForm(): AnnouncementCreateReq {
  return {
    title: '',
    content: '',
    level: 'info',
    link_url: '',
    link_text: '',
    pinned: false,
    enabled: true,
    sort_order: 0,
  };
}

export default function AnnouncementsPage() {
  const qc = useQueryClient();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Announcement | null>(null);
  const [form, setForm] = useState<AnnouncementCreateReq>(blankForm());

  const query = useQuery({
    queryKey: ['admin', 'announcements'],
    queryFn: () => announcementsApi.list({ page: 1, page_size: 100 }),
  });
  const items = query.data?.list ?? [];

  const openCreate = () => {
    setEditing(null);
    setForm(blankForm());
    setFormOpen(true);
  };
  const openEdit = (a: Announcement) => {
    setEditing(a);
    setForm({
      title: a.title,
      content: a.content,
      level: a.level,
      link_url: a.link_url ?? '',
      link_text: a.link_text ?? '',
      pinned: a.pinned,
      enabled: a.enabled,
      sort_order: a.sort_order,
    });
    setFormOpen(true);
  };

  const save = useMutation({
    mutationFn: () =>
      editing
        ? announcementsApi.update(editing.id, {
            ...form,
            link_url: form.link_url || undefined,
            link_text: form.link_text || undefined,
          })
        : announcementsApi.create({
            ...form,
            link_url: form.link_url || undefined,
            link_text: form.link_text || undefined,
          }),
    onSuccess: () => {
      toast.success(editing ? '公告已更新' : '公告已发布');
      setFormOpen(false);
      qc.invalidateQueries({ queryKey: ['admin', 'announcements'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const remove = useMutation({
    mutationFn: (id: number) => announcementsApi.remove(id),
    onSuccess: () => {
      toast.success('公告已删除');
      qc.invalidateQueries({ queryKey: ['admin', 'announcements'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const toggleEnabled = (a: Announcement) =>
    announcementsApi.update(a.id, { enabled: !a.enabled }).then(() => {
      toast.success(a.enabled ? '公告已停用' : '公告已启用');
      qc.invalidateQueries({ queryKey: ['admin', 'announcements'] });
    }).catch((e: ApiError) => toast.error(e.message));

  const set = <K extends keyof AnnouncementCreateReq>(k: K, v: AnnouncementCreateReq[K]) =>
    setForm((f) => ({ ...f, [k]: v }));

  return (
    <PageShell>
      <PageHeader
        icon={<Megaphone size={16} />}
        title="系统公告"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={() => qc.invalidateQueries({ queryKey: ['admin', 'announcements'] })}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-primary btn-sm" onClick={openCreate}>
              <Plus size={14} /> 发布公告
            </button>
          </>
        }
      />

      <Section>
        <p className="mb-4 text-small text-text-secondary">
          公告显示在用户端首页顶部滚动条，支持多条循环展示、置顶、时间窗控制。
          启用状态的公告在用户端 <strong>1 分钟内</strong>自动刷新可见。
        </p>
        {query.isLoading ? (
          <div className="py-10 text-center text-text-tertiary">加载中...</div>
        ) : items.length === 0 ? (
          <div className="py-10 text-center text-text-tertiary">
            暂无公告，点击「发布公告」创建第一条。
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="data-table text-small">
              <thead>
                <tr>
                  <th className="w-8">置顶</th>
                  <th>标题</th>
                  <th className="w-20">级别</th>
                  <th className="w-16">状态</th>
                  <th className="w-20">排序</th>
                  <th className="w-36">创建时间</th>
                  <th className="w-28">操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((a) => (
                  <tr key={a.id} className={!a.enabled ? 'opacity-50' : ''}>
                    <td className="text-center">{a.pinned ? '📌' : ''}</td>
                    <td>
                      <div className="font-medium text-text-primary">{a.title}</div>
                      {a.content && a.content !== a.title && (
                        <div className="mt-0.5 truncate text-tiny text-text-tertiary max-w-xs">{a.content}</div>
                      )}
                      {a.link_url && (
                        <a href={a.link_url} target="_blank" rel="noreferrer" className="text-tiny text-klein-500 hover:underline">
                          {a.link_text || a.link_url}
                        </a>
                      )}
                    </td>
                    <td><span className={LEVEL_BADGE[a.level]}>{a.level}</span></td>
                    <td>
                      <button
                        className={`badge cursor-pointer ${a.enabled ? 'badge-success' : 'badge-outline'}`}
                        onClick={() => toggleEnabled(a)}
                        title={a.enabled ? '点击停用' : '点击启用'}
                      >
                        {a.enabled ? '启用' : '停用'}
                      </button>
                    </td>
                    <td className="text-text-secondary">{a.sort_order}</td>
                    <td className="text-text-tertiary whitespace-nowrap">{fmtTime(a.created_at)}</td>
                    <td>
                      <div className="inline-flex gap-1">
                        <button className="btn btn-ghost btn-icon btn-sm" title="编辑" onClick={() => openEdit(a)}>
                          <Pencil size={14} />
                        </button>
                        <button
                          className="btn btn-ghost btn-icon btn-sm text-danger"
                          title="删除"
                          onClick={() => {
                            if (confirm(`确认删除公告「${a.title}」？`)) remove.mutate(a.id);
                          }}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      {formOpen && (
        <div className="fixed inset-0 z-[80] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
          <div className="dialog-surface klein-fade-in w-full max-w-lg">
            <header className="modal-header">
              <h2 className="text-h4">{editing ? '编辑公告' : '发布公告'}</h2>
              <button className="btn btn-ghost btn-sm" onClick={() => setFormOpen(false)}>
                <X size={16} />
              </button>
            </header>
            <div className="modal-body space-y-4">
              {/* 标题 */}
              <div>
                <label className="field-label mb-1 block">标题 <span className="text-danger">*</span></label>
                <input
                  className="input w-full"
                  placeholder="公告标题（显示在滚动条最显眼位置）"
                  value={form.title}
                  onChange={(e) => set('title', e.target.value)}
                  maxLength={128}
                />
              </div>
              {/* 正文 */}
              <div>
                <label className="field-label mb-1 block">正文（可选）</label>
                <textarea
                  className="textarea w-full"
                  rows={2}
                  placeholder="附加说明，标题后面展示"
                  value={form.content}
                  onChange={(e) => set('content', e.target.value)}
                />
              </div>
              {/* 级别 + 置顶 */}
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="field-label mb-1 block">级别</label>
                  <select
                    className="select w-full"
                    value={form.level}
                    onChange={(e) => set('level', e.target.value as AnnouncementLevel)}
                  >
                    {(Object.keys(LEVEL_LABELS) as AnnouncementLevel[]).map((l) => (
                      <option key={l} value={l}>{LEVEL_LABELS[l]}</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="field-label mb-1 block">排序数字（小在前）</label>
                  <input
                    className="input w-full"
                    type="number"
                    value={form.sort_order}
                    onChange={(e) => set('sort_order', Number(e.target.value))}
                  />
                </div>
              </div>
              {/* 跳转链接 */}
              <div>
                <label className="field-label mb-1 block">跳转链接（可选）</label>
                <input
                  className="input w-full"
                  placeholder="https://... 或 /docs 等内部路径"
                  value={form.link_url}
                  onChange={(e) => set('link_url', e.target.value)}
                />
              </div>
              <div>
                <label className="field-label mb-1 block">链接按钮文字（可选）</label>
                <input
                  className="input w-full"
                  placeholder="默认「查看详情」"
                  value={form.link_text}
                  onChange={(e) => set('link_text', e.target.value)}
                  maxLength={64}
                />
              </div>
              {/* 置顶 + 启用 */}
              <div className="flex items-center gap-6">
                <label className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    className="checkbox"
                    checked={form.pinned}
                    onChange={(e) => set('pinned', e.target.checked)}
                  />
                  <span className="text-small">置顶</span>
                </label>
                <label className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    className="checkbox"
                    checked={form.enabled}
                    onChange={(e) => set('enabled', e.target.checked)}
                  />
                  <span className="text-small">立即启用</span>
                </label>
              </div>
            </div>
            <footer className="modal-footer">
              <button className="btn btn-outline btn-md" onClick={() => setFormOpen(false)}>取消</button>
              <button
                className="btn btn-primary btn-md"
                disabled={!form.title.trim() || save.isPending}
                onClick={() => save.mutate()}
              >
                {save.isPending ? '保存中...' : editing ? '保存修改' : '发布'}
              </button>
            </footer>
          </div>
        </div>
      )}
    </PageShell>
  );
}
