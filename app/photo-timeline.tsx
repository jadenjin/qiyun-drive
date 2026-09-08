"use client";
/* eslint-disable @next/next/no-img-element */

import { useEffect, useMemo, useRef, useState } from "react";

type TimelinePhoto = { nodeId: string; name: string; createdAt: string; takenAt?: string | null; thumbUrl?: string; remark?: string; tone?: string };

export function PhotoTimeline<T extends TimelinePhoto>({ photos, onOpen }: { photos: T[]; onOpen: (photo: T) => void }) {
  const viewport = useRef<HTMLDivElement>(null);
  const [layout, setLayout] = useState({ width: 800, height: 600, top: 0 });
  useEffect(() => {
    const element = viewport.current;
    if (!element) return;
    const resize = new ResizeObserver(() => setLayout(current => ({ ...current, width: element.clientWidth, height: element.clientHeight, top: element.scrollTop })));
    resize.observe(element);
    return () => resize.disconnect();
  }, []);
  const columns = Math.max(2, Math.min(6, Math.floor(layout.width / 190)));
  const { rows, height } = useMemo(() => {
    const groups = new Map<string, T[]>();
    const formatter = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric" });
    for (const photo of photos) {
      const date = new Date(photo.takenAt || photo.createdAt);
      const label = Number.isNaN(date.getTime()) ? "未知日期" : formatter.format(date);
      const group = groups.get(label) || [];
      group.push(photo);
      groups.set(label, group);
    }
    const rows: { key: string; top: number; height: number; label?: string; photos?: T[] }[] = [];
    let height = 0;
    for (const [label, group] of groups) {
      rows.push({ key: label, label, top: height, height: 42 });
      height += 42;
      for (let offset = 0; offset < group.length; offset += columns) {
        rows.push({ key: group[offset].nodeId, top: height, height: 200, photos: group.slice(offset, offset + columns) });
        height += 200;
      }
    }
    return { rows, height };
  }, [photos, columns]);
  const visibleRows = rows.filter(row => row.top + row.height >= layout.top - 400 && row.top <= layout.top + layout.height + 400);
  return <section className="photo-view">
    <div className="photo-summary"><span><strong>已加载 {photos.length} 张照片</strong><small>按拍摄日期整理，向下滚动浏览</small></span></div>
    {/* Keyboard focus lets arrow/PageDown keys scroll to photos outside the virtual window. */}
    {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
    <div ref={viewport} className="photo-timeline-viewport" role="region" aria-label="照片时间线" tabIndex={0} onScroll={event => { const top = event.currentTarget.scrollTop; setLayout(current => ({ ...current, top })); }}>
      <div style={{ position: "relative", height: Math.max(height, 100) }}>
        {visibleRows.map(row => <div key={row.key} style={{ position: "absolute", top: row.top, height: row.height, left: 0, right: 0 }}>
          {row.label ? <div className="photo-date"><h2>{row.label}</h2></div> : <div style={{ display: "grid", gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`, gap: 10, height: 190 }}>
            {row.photos?.map(photo => <button type="button" className={`photo-card photo-${photo.tone || "default"}`} key={photo.nodeId} aria-label={photo.remark || photo.name} onClick={() => onOpen(photo)}>
              {photo.thumbUrl ? <img src={photo.thumbUrl} alt={photo.remark || photo.name} loading="lazy" decoding="async" /> : <div className="photo-art"><span>{photo.name}</span></div>}
              <div className="photo-overlay"><strong>{photo.remark || photo.name}</strong><small>{photo.name}</small></div>
            </button>)}
          </div>}
        </div>)}
        {!photos.length && <p>还没有加载到照片，可以上传照片或继续加载更早的记录。</p>}
      </div>
    </div>
  </section>;
}
