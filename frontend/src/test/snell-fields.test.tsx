import { describe, expect, it } from 'vitest';
import { act, fireEvent, screen } from '@testing-library/react';
import { Form } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import { createDefaultInboundSettings } from '@/lib/xray/inbound-defaults';
import { formValuesToWirePayload, rawInboundToFormValues } from '@/lib/xray/inbound-form-adapter';
import { buildSnellSurgeLine, canCopySnellSurge } from '@/pages/inbounds/form/protocols/snell';
import { InboundFormSchema } from '@/schemas/forms/inbound-form';
import { Protocols } from '@/schemas/primitives';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import SnellFields from '@/pages/inbounds/form/protocols/snell';
import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { DBInbound } from '@/models/dbinbound';
import { renderWithProviders } from './test-utils';

function SnellFieldsHarness() {
  const methods = useForm<InboundFormValues>({
    defaultValues: {
      protocol: 'snell',
      remark: 'edge',
      port: 443,
      settings: { psk: 'first-psk-value-123' },
    } as InboundFormValues,
  });
  return (
    <FormProvider {...methods}>
      <Form><SnellFields host="198.51.100.4" /></Form>
    </FormProvider>
  );
}

describe('Snell inbound form contract', () => {
  it('registers Snell and creates a fresh secure PSK', () => {
    expect(Protocols.SNELL).toBe('snell');
    const settings = createDefaultInboundSettings('snell');
    expect(settings).toMatchObject({ psk: expect.any(String) });
    expect((settings as { psk: string }).psk).toHaveLength(32);
    expect(InboundFormSchema.safeParse({ port: 443, protocol: 'snell', settings }).success).toBe(true);
  });

  it('keeps an empty edit PSK on the wire for backend preservation semantics', () => {
    const values = rawInboundToFormValues({
      port: 443,
      protocol: 'snell',
      settings: { psk: 'existing-psk-123456' },
    });
    const payload = formValuesToWirePayload({ ...values, settings: { psk: '' } } as never);
    expect(JSON.parse(payload.settings)).toEqual({ psk: '' });
  });

  it('omits all Xray stream and sniffing payloads for Snell', () => {
    const values = rawInboundToFormValues({
      port: 443,
      protocol: 'snell',
      settings: { psk: 'existing-psk-123456' },
      streamSettings: { network: 'tcp', security: 'tls', tcpSettings: {} },
      sniffing: { enabled: true },
    });
    const payload = formValuesToWirePayload(values);
    expect(payload.streamSettings).toBe('');
    expect(payload.sniffing).toBe('');
  });

  it('builds exactly the Surge v5 line and rejects incomplete copy inputs', () => {
    expect(buildSnellSurgeLine('edge', '198.51.100.4', 443, 'abc1234567890123'))
      .toBe('edge = snell, 198.51.100.4, 443, psk=abc1234567890123, version=5');
    expect(canCopySnellSurge('198.51.100.4', 443, 'abc1234567890123')).toBe(true);
    expect(canCopySnellSurge('', 443, 'abc1234567890123')).toBe(false);
    expect(canCopySnellSurge('host', 0, 'abc1234567890123')).toBe(false);
    expect(canCopySnellSurge('host', 65536, 'abc1234567890123')).toBe(false);
    expect(canCopySnellSurge('host', 443, '')).toBe(false);
  });

  it('changes the PSK only when the explicit regenerate button is clicked', () => {
    renderWithProviders(<SnellFieldsHarness />);
    const input = screen.getByLabelText('PSK') as HTMLInputElement;
    expect(input.value).toBe('first-psk-value-123');
    fireEvent.click(screen.getByRole('button', { name: 'Regenerate PSK' }));
    expect(input.value).not.toBe('first-psk-value-123');
    expect(input.value).toHaveLength(32);
  });

  it('shows only the Snell controls outside the public basic fields', async () => {
    renderWithProviders(
      <InboundFormModal
        open
        mode="edit"
        dbInbound={new DBInbound({
          id: 1,
          protocol: 'snell',
          port: 443,
          remark: 'edge',
          settings: { psk: 'existing-psk-123456' },
          enable: true,
        })}
        dbInbounds={[]}
        availableNodes={[]}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(screen.getByRole('tab', { name: 'Protocol' })).toBeTruthy();
    expect(screen.queryByRole('tab', { name: /stream/i })).toBeNull();
    expect(screen.queryByRole('tab', { name: /security/i })).toBeNull();
    expect(screen.queryByRole('tab', { name: /sniffing/i })).toBeNull();
    expect(screen.queryByRole('tab', { name: /advanced/i })).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Protocol' }));
    expect(await screen.findByLabelText('PSK')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy Surge v5' })).toBeTruthy();
  });
});
