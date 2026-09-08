import { useCallback, useEffect, useRef, useState } from 'react';
import { Popconfirm, Popover, Switch } from 'antd';
import { CircleStopIcon, EllipsisIcon, LoaderIcon, RotateCwIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import * as api from '@/api/extensions/netbird.ts';
import * as vpnApi from '@/api/extensions/vpn.ts';

import { ErrorHelp } from './error-help.tsx';
import type { State } from './types.ts';
import { Uninstall } from './uninstall.tsx';

type HeaderProps = {
  state: State | undefined;
  statusIsFresh: boolean;
  onSuccess: () => void;
};

type Loading = '' | 'restarting' | 'stopping';

export const Header = ({ state, statusIsFresh, onSuccess }: HeaderProps) => {
  const { t } = useTranslation();

  const [loading, setLoading] = useState<Loading>('');
  const [isAutostart, setIsAutostart] = useState(false);
  const [autostartLoading, setAutostartLoading] = useState(false);
  const [errMsg, setErrMsg] = useState('');
  const isMounted = useRef(true);
  const preferenceRequestId = useRef(0);
  const autostartOperationId = useRef(0);
  const hasKnownInstalledState = !!state && state !== 'notInstall';
  // A failed status request must not hide recovery actions. Stop/Restart are
  // deliberately useful precisely when the daemon cannot be observed.
  const showRecoveryActions = state !== 'notInstall';

  const refreshPreference = useCallback(async (reportError = true): Promise<string | undefined> => {
    const currentRequestId = ++preferenceRequestId.current;
    try {
      const rsp: any = await vpnApi.getPreference();
      if (!isMounted.current || currentRequestId !== preferenceRequestId.current) return undefined;

      if (rsp.code !== 0) {
        if (reportError) setErrMsg(rsp.msg);
        return undefined;
      }

      const vpn = rsp.data?.vpn;
      setIsAutostart(vpn === 'netbird');
      setErrMsg('');
      return vpn;
    } catch (err: any) {
      if (!isMounted.current || currentRequestId !== preferenceRequestId.current) return undefined;
      if (reportError) setErrMsg(err?.message || 'Failed to read VPN preference');
      return undefined;
    }
  }, []);

  useEffect(() => {
    // Invalidating both request counters prevents a late response from a
    // previous Settings mount from changing the newly mounted panel.
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
    setErrMsg('');

    try {
      const rsp: any = await vpnApi.setPreference('netbird');
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      // Every failure code arrives with HTTP 200, so flipping the switch on a
      // resolved request alone would report success while the device may have
      // no VPN running at all.
      if (rsp.code !== 0) {
        setErrMsg(rsp.msg);
        return;
      }

      setIsAutostart(true);
      onSuccess();
    } catch {
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      // A transport timeout does not cancel the server handler. Do not retry a
      // state-changing request: re-read its authoritative result instead.
      setErrMsg(t('settings.netbird.preferenceUnknown'));
      const vpn = await refreshPreference(false);
      if (!isMounted.current || currentOperationId !== autostartOperationId.current) return;

      if (vpn === 'netbird') {
        onSuccess();
      } else if (vpn) {
        setErrMsg(t('settings.netbird.preferenceNotChanged'));
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

    setErrMsg('');

    api
      .restart()
      .then((rsp) => {
        if (rsp.code !== 0) {
          setErrMsg(rsp.msg);
        }
      })
      .catch((err) => {
        setErrMsg(err.message || 'Restart failed');
      })
      .finally(() => {
        setLoading('');
        onSuccess();
      });
  }

  function stop() {
    if (loading !== '') return;
    setLoading('stopping');

    setErrMsg('');

    api
      .stop()
      .then((rsp) => {
        if (rsp.code !== 0) {
          setErrMsg(rsp.msg);
        }
      })
      .catch((err) => {
        setErrMsg(err.message || 'Stop failed');
      })
      .finally(() => {
        setLoading('');
        onSuccess();
      });
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex items-center space-x-2">
          <span className="text-base">{t('settings.netbird.title')}</span>

          {hasKnownInstalledState && (
            <Popconfirm
              title={t('settings.netbird.autostartConfirm')}
              description={
                <div className="max-w-[320px] text-xs text-neutral-400">
                  {t('settings.netbird.autostartWarning')}
                </div>
              }
              onConfirm={() => handleAutostartChange(true)}
              okText={t('settings.netbird.okBtn')}
              cancelText={t('settings.netbird.cancelBtn')}
              placement="bottom"
              disabled={isAutostart || autostartLoading}
            >
              <Switch
                checked={isAutostart}
                loading={autostartLoading}
                size="small"
                title={t('settings.netbird.autostart')}
                disabled={!statusIsFresh || isAutostart || autostartLoading}
              />
            </Popconfirm>
          )}
        </div>

        <div className="flex items-center space-x-2">
          {showRecoveryActions && (
            <>
              {/* restart button */}
              <Popconfirm
                title={t('settings.netbird.restart')}
                onConfirm={restart}
                okText={t('settings.netbird.okBtn')}
                cancelText={t('settings.netbird.cancelBtn')}
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
                title={t('settings.netbird.stop')}
                description={
                  <div className="max-w-[320px] space-y-1">
                    <div>{t('settings.netbird.stopDesc')}</div>
                    <div className="text-xs text-neutral-400">
                      {t('settings.netbird.stopWarning')}
                    </div>
                  </div>
                }
                onConfirm={stop}
                okText={t('settings.netbird.okBtn')}
                cancelText={t('settings.netbird.cancelBtn')}
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

              {/* Do not expose irreversible uninstall until there is a
                  confirmed installed state; Stop/Restart above stay available
                  for an unknown status as recovery actions. */}
              {hasKnownInstalledState && (
                <Popover
                  content={<Uninstall onSuccess={onSuccess} />}
                  placement="bottomRight"
                  arrow={false}
                >
                  <div className="flex cursor-pointer rounded p-1 text-neutral-300 hover:bg-neutral-600">
                    <EllipsisIcon size={18} />
                  </div>
                </Popover>
              )}
            </>
          )}
        </div>
      </div>

      {errMsg && <ErrorHelp error={errMsg} onRefresh={onSuccess} />}
    </>
  );
};
