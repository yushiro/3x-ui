import { z } from 'zod';

// Snell v5 has exactly one inbound secret. It is intentionally separate from
// the Xray client-list schemas because sidecars never create ClientTraffic.
export const SnellInboundSettingsSchema = z.object({
  psk: z.string().min(16),
});
export type SnellInboundSettings = z.infer<typeof SnellInboundSettingsSchema>;
