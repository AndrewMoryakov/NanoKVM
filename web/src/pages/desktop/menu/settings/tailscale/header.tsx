import { useCallback, useEffect, useRef, useState } from 'react';
import { message, Popconfirm, Popover, Switch } from 'antd';
import { CircleStopIcon, EllipsisIcon, LoaderIcon, RotateCwIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import * as api from '@/api/extensions/tailscale.ts';
import * as vpnApi from '@/api/extensions/vpn.ts';

import { Memory } from './memory.tsx';
import { Swap } from './swap.tsx';
import type { State } from './types.ts';
import { Uninstall } from './uninstall.tsx';

type HeaderProps = {
  state: State | undefined;
  onSuccess: () => void;
};

type Loading = '' | 'restarting' | 'stopping';

export const Header = ({ state, onSuccess }: HeaderProps) => {
  const { t } = useTranslation();

  const [loading, setLoading] = useState<Loading>('');
  const [isAutostart, setIsAutostart] = useState(false);
  const [autostartLoading, setAutostartLoading] = useState(false);
  const isMounted = useRef(true);
  const preferenceRequestId = useRef(0);
  const autostartOperationId = useRef(0);

  const refreshPreference = useCallback(async (reportError = true): Promise<string | undefined> => {
    const currentRequestId = ++preferenceRequestId.current;
    try {
      const rsp: any = await vpnApi.getPreference();
      if (!isMounted.current || currentRequestId !== preferenceRequestId.current) return undefined;

      if (rsp.code !== 0) {
        if (reportError) message.error(rsp.msg);
        return undefined;
      }

      const vpn = rsp.data?.vpn;
      setIsAutostart(vpn === 'tailscale');
      return vpn;
    } catch {
      if (!isMounted.current || currentRequestId !== preferenceRequestId.current) return undefined;
      // Leave the switch off as the safe default. A failed mutation has already
      // shown its unknown-result warning before this reconciliation read.
      return undefined;
    }
  }, []);

  useEffect(() => {
    isMounted.current = true;
    void refreshPreference();

    return () => {
      isMounted.current = false;
      preferenceRequestId.current += 1;
      autostartOperationId.current += 1;
    };
  }, [refreshPreference]);

  async function handleAutostartChange(checked: boolean) {
    if (!checked || autostartLoading) return;
    const currentOperationId = ++autostartOperationId.current;
    // A request that began before the mutation cannot authoritatively update
    // the switch after it completes.
    preferenceRequestId.current += 1;
    setAutostartLoading(true);

    try {
      const rsp: any = await vpnApi.setPreference('tailscale');
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      // Failure codes arrive with HTTP 200, so switching on a resolved request
      // alone would show autostart as enabled while the device may have no VPN
      // running.
      if (rsp.code !== 0) {
        message.error(rsp.msg);
        return;
      }

      setIsAutostart(true);
      onSuccess();
    } catch {
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      // The server may still be completing the requested switch. Re-read
      // instead of issuing an automatic second state-changing request.
      message.warning(t('settings.tailscale.preferenceUnknown'));
      const vpn = await refreshPreference(false);
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      if (vpn === 'tailscale') {
        onSuccess();
      } else if (vpn) {
        message.error(t('settings.tailscale.preferenceNotChanged'));
      }
    } finally {
      if (isMounted.current && currentOperationId === autostartOperationId.current) {
        setAutostartLoading(false);
      }
    }
  }

  function restart() {
    if (loading !== '') return;
    setLoading('restarting');

    api.restart().finally(() => {
      setLoading('');
      onSuccess();
    });
  }

  function stop() {
    if (loading !== '') return;
    setLoading('stopping');

    api.stop().finally(() => {
      setLoading('');
      onSuccess();
    });
  }

  return (
    <div className="flex items-center justify-between">
      <div className="flex items-center space-x-2">
        <span className="text-base">{t('settings.tailscale.title')}</span>

        {state && state !== 'notInstall' && (
          <Popconfirm
            title={t('settings.tailscale.autostartConfirm')}
            description={
              <div className="max-w-[320px] text-xs text-neutral-400">
                {t('settings.tailscale.autostartWarning')}
              </div>
            }
            onConfirm={() => handleAutostartChange(true)}
            okText={t('settings.tailscale.okBtn')}
            cancelText={t('settings.tailscale.cancelBtn')}
            placement="bottom"
            disabled={isAutostart || autostartLoading}
          >
            <Switch
              checked={isAutostart}
              loading={autostartLoading}
              size="small"
              title={t('settings.tailscale.autostart')}
              disabled={isAutostart || autostartLoading}
            />
          </Popconfirm>
        )}
      </div>

      <div className="flex items-center space-x-2">
        {state && ['notRunning', 'notLogin', 'stopped', 'running'].includes(state) && (
          <>
            {/* restart button */}
            <Popconfirm
              title={t('settings.tailscale.restart')}
              onConfirm={restart}
              okText={t('settings.tailscale.okBtn')}
              cancelText={t('settings.tailscale.cancelBtn')}
              placement="bottom"
              disabled={loading !== ''}
            >
              <div className="flex cursor-pointer rounded p-1 text-green-500 hover:bg-neutral-600 hover:text-green-500/80">
                {loading === 'restarting' ? (
                  <LoaderIcon className="animate-spin" size={18} />
                ) : (
                  <RotateCwIcon size={18} />
                )}
              </div>
            </Popconfirm>

            {/* stop button */}
            <Popconfirm
              title={t('settings.tailscale.stop')}
              description={
                <div className="max-w-[320px] space-y-1">
                  <div>{t('settings.tailscale.stopDesc')}</div>
                  <div className="text-xs text-neutral-400">
                    {t('settings.tailscale.stopWarning')}
                  </div>
                </div>
              }
              onConfirm={stop}
              okText={t('settings.tailscale.okBtn')}
              cancelText={t('settings.tailscale.cancelBtn')}
              placement="bottom"
              disabled={loading !== ''}
            >
              <div className="flex cursor-pointer rounded p-1 text-red-500 hover:bg-neutral-600 hover:text-red-500/80">
                {loading === 'stopping' ? (
                  <LoaderIcon className="animate-spin" size={18} />
                ) : (
                  <CircleStopIcon size={18} />
                )}
              </div>
            </Popconfirm>
          </>
        )}

        {/* more button */}
        {state && state !== 'notInstall' && (
          <Popover
            content={
              <div className="flex min-w-[250px] flex-col">
                <Memory />
                <Swap />
                <Uninstall onSuccess={onSuccess} />
              </div>
            }
            placement="bottom"
            trigger="click"
          >
            <div className="flex cursor-pointer rounded p-1 text-white hover:bg-neutral-700/50">
              <EllipsisIcon size={18} />
            </div>
          </Popover>
        )}
      </div>
    </div>
  );
};
