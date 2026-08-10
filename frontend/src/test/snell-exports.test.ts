import { describe, expect, it } from 'vitest';

import {
  genAllLinks,
  genInboundLinks,
  getInboundClients,
  hasSnellSurgeCopy,
  isSnell,
} from '@/lib/xray/inbound-link';
import { buildRowActionsMenu } from '@/pages/inbounds/list/RowActions';
import { isInboundMultiUser, showQrCodeMenu } from '@/pages/inbounds/list/helpers';
import type { Inbound } from '@/schemas/api/inbound';

const snellInbound = {
  port: 443,
  protocol: 'snell',
  settings: { psk: '0123456789abcdef0123456789abcdef' },
} as Inbound;

describe('Snell export boundaries', () => {
  it('keeps Snell out of generic links, QR, and client-management actions', () => {
    expect(isSnell('snell')).toBe(true);
    expect(hasSnellSurgeCopy('snell')).toBe(true);
    expect(getInboundClients(snellInbound)).toBeNull();
    expect(genAllLinks({
      inbound: snellInbound,
      remark: 'edge',
      client: {},
      fallbackHostname: 'edge.example.com',
    })).toEqual([]);
    expect(genInboundLinks({
      inbound: snellInbound,
      remark: 'edge',
      fallbackHostname: 'edge.example.com',
    })).toBe('');

    const record = {
      id: 1,
      enable: true,
      remark: 'edge',
      subSortIndex: 1,
      port: 443,
      protocol: 'snell',
      up: 0,
      down: 0,
      total: 0,
      expiryTime: 0,
      _expiryTime: null,
      settings: { psk: '0123456789abcdef0123456789abcdef' },
      streamSettings: {},
    };
    expect(showQrCodeMenu(record)).toBe(false);
    expect(isInboundMultiUser(record)).toBe(false);
    const actions = buildRowActionsMenu({
      record,
      subEnable: true,
      hasClients: false,
      t: (key) => key,
    });
    expect(actions).not.toContainEqual(expect.objectContaining({ key: 'qrcode' }));
    expect(actions).not.toContainEqual(expect.objectContaining({ key: 'clipboard' }));
    expect(actions).toContainEqual(expect.objectContaining({ key: 'showInfo' }));
  });
});
