# 标志源文件

- `tillbox.svg`：标志主文件，也直接用作 `public/favicon.svg`。
- `tillbox-16.svg`：16 像素专用版，线条按像素对齐，小尺寸更清楚。
- `maskable.svg`：满版无圆角，图形缩在安全区内，给安卓自适应图标和 iOS 主屏幕用。

改完源文件后，在 `web/` 目录下重新导出（需要 `rsvg-convert` 和 ImageMagick 7）：

```sh
cp icons/tillbox.svg public/favicon.svg
rsvg-convert -w 16 -h 16 icons/tillbox-16.svg -o /tmp/tb-16.png
rsvg-convert -w 32 -h 32 icons/tillbox.svg -o /tmp/tb-32.png
rsvg-convert -w 48 -h 48 icons/tillbox.svg -o /tmp/tb-48.png
magick /tmp/tb-16.png /tmp/tb-32.png /tmp/tb-48.png public/favicon.ico
rsvg-convert -w 192 -h 192 icons/tillbox.svg -o public/icon-192.png
rsvg-convert -w 512 -h 512 icons/tillbox.svg -o public/icon-512.png
rsvg-convert -w 512 -h 512 icons/maskable.svg -o public/icon-maskable-512.png
rsvg-convert -w 180 -h 180 icons/maskable.svg -o public/apple-touch-icon.png
```
