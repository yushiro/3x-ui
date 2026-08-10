import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';

import { DBInbound } from '@/models/dbinbound';
import InboundInfoModal from '@/pages/inbounds/info/InboundInfoModal';
import { renderWithProviders } from './test-utils';

describe('Snell inbound information', () => {
  it('shows shared traffic and runtime state without a client count', async () => {
    renderWithProviders(
      <InboundInfoModal
        open
        onClose={() => {}}
        dbInbound={new DBInbound({
          id: 1,
          protocol: 'snell',
          port: 443,
          remark: 'edge',
          enable: true,
          up: 1024,
          down: 2048,
          total: 8192,
          settings: { psk: '0123456789abcdef0123456789abcdef' },
          runtimeStatus: { running: true, errorCategory: 'missing_binary' },
        })}
      />,
    );

    expect(await screen.findByText('running')).toBeTruthy();
    expect(screen.getByText('missing_binary')).toBeTruthy();
    expect(screen.getByText('Traffic')).toBeTruthy();
    expect(screen.queryByRole('tab', { name: 'Client' })).toBeNull();
  });
});
