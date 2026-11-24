// TypeScript 5.6 自带的 DOM 类型里有 VideoEncoder，还没有音频编码这一半（WebCodecs 的 AudioEncoder、AudioData）。
// 高光视频（charts/film.ts）只用到下面这些，按 W3C WebCodecs 规范补上。
interface AudioEncoderConfig {
  codec: string
  sampleRate: number
  numberOfChannels: number
  bitrate?: number
}

interface AudioEncoderSupport { supported?: boolean; config?: AudioEncoderConfig }

interface EncodedAudioChunkMetadata { decoderConfig?: { codec: string; sampleRate: number; numberOfChannels: number; description?: BufferSource } }

interface EncodedAudioChunk {
  readonly type: 'key' | 'delta'
  readonly timestamp: number
  readonly duration: number | null
  readonly byteLength: number
  copyTo(destination: BufferSource): void
}

interface AudioDataInit {
  format: 'f32-planar' | 'f32' | 's16' | 's16-planar' | 'u8' | 'u8-planar' | 's32' | 's32-planar'
  sampleRate: number
  numberOfFrames: number
  numberOfChannels: number
  timestamp: number
  data: BufferSource
}

declare class AudioData {
  constructor(init: AudioDataInit)
  readonly numberOfFrames: number
  close(): void
}

declare class AudioEncoder {
  constructor(init: { output: (chunk: EncodedAudioChunk, meta?: EncodedAudioChunkMetadata) => void; error: (e: DOMException) => void })
  static isConfigSupported(config: AudioEncoderConfig): Promise<AudioEncoderSupport>
  readonly encodeQueueSize: number
  configure(config: AudioEncoderConfig): void
  encode(data: AudioData): void
  flush(): Promise<void>
  close(): void
}
