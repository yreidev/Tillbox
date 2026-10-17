// 识别图片里的二维码（经营码截图）。zxing 的 wasm 打包进本站，不从外部 CDN 加载；只在用到时才下载。
export async function decodeQRImage(file: Blob): Promise<string[]> {
  const [{ prepareZXingModule, readBarcodes }, { default: wasmUrl }] = await Promise.all([
    import('zxing-wasm/reader'),
    import('zxing-wasm/reader/zxing_reader.wasm?url'),
  ])
  prepareZXingModule({
    overrides: { locateFile: (path: string, prefix: string) => (path.endsWith('.wasm') ? wasmUrl : prefix + path) },
  })
  const results = await readBarcodes(file, { formats: ['QRCode'], tryHarder: true, maxNumberOfSymbols: 4 })
  return results.filter((r) => r.isValid && r.text).map((r) => r.text)
}
