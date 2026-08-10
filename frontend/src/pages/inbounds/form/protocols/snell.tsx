import { Button, Input, Space, Tooltip } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { ClipboardManager, RandomUtil } from '@/utils';

export function buildSnellSurgeLine(name: string, host: string, port: number, psk: string): string {
  return `${name} = snell, ${host}, ${port}, psk=${psk}, version=5`;
}

export function canCopySnellSurge(host: string, port: number, psk: string): boolean {
  return host.trim() !== '' && Number.isInteger(port) && port >= 1 && port <= 65535 && psk.trim() !== '';
}

interface SnellFieldsProps {
  host: string;
}

export default function SnellFields({ host }: SnellFieldsProps) {
  const { control, setValue } = useFormContext();
  const psk = (useWatch({ control, name: 'settings.psk' }) ?? '') as string;
  const remark = (useWatch({ control, name: 'remark' }) ?? '') as string;
  const port = useWatch({ control, name: 'port' });
  const copyReady = canCopySnellSurge(host, typeof port === 'number' ? port : 0, psk);
  const regeneratePsk = () => setValue('settings.psk', RandomUtil.randomSeq(32), { shouldDirty: true });
  const missing = [
    host.trim() === '' ? 'host' : '',
    !Number.isInteger(port) || (port as number) < 1 || (port as number) > 65535 ? 'port' : '',
    psk.trim() === '' ? 'PSK' : '',
  ].filter(Boolean).join(', ');

  return (
    <>
      <FormField name={['settings', 'psk']} label="PSK">
        <Input.Password autoComplete="new-password" />
      </FormField>
      <Space>
        <Button onClick={regeneratePsk}>{psk ? 'Regenerate PSK' : 'Generate PSK'}</Button>
        <Tooltip title={copyReady ? undefined : `Missing ${missing}`}>
          <Button
            disabled={!copyReady}
            onClick={() => { void ClipboardManager.copyText(buildSnellSurgeLine(remark, host, port as number, psk)); }}
          >
            Copy Surge v5
          </Button>
        </Tooltip>
      </Space>
    </>
  );
}
