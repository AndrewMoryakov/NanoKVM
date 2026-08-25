import { useEffect, useState } from 'react';
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
  onSuccess: () => void;
};

type Loading = '' | 'restarting' | 'stopping';

export const Header = ({ state, onSuccess }: HeaderProps) => {
  const { t } = useTranslation();

  const [loading, setLoading] = useState<Loading>('');
  const [isAutostart, setIsAutostart] = useState(false);
  const [autostartLoading, setAutostartLoading] = useState(false);
  const [errMsg, setErrMsg] = useState('');

  useEffect(() => {
    vpnApi
      .getPreference()
      .then((rsp: any) => {
        if (rsp.code !== 0) {
          setErrMsg(rsp.msg);
          return;
        }

        if (rsp.data?.vpn) {
          setIsAutostart(rsp.data.vpn === 'netbird');
        }
      })
      .catch((err) => {
        setErrMsg(err.message || 'Failed to read VPN preference');
      });
  }, []);

  function handleAutostartChange(checked: boolean) {
    if (!checked || autostartLoading) return;
    setAutostartLoading(true);

    setErrMsg('');

    vpnApi
      .setPreference('netbird')
      // Every failure code (-1..-5) arrives with HTTP 200, so flipping the
      // switch on `then` alone reports success while the device may have no
      // VPN running at all.
      .then((rsp: any) => {
        if (rsp.code !== 0) {
          setErrMsg(rsp.msg);
          return;
        }

        setIsAutostart(true);
        onSuccess();
      })
      .catch((err) => {
        setErrMsg(err.message || 'Failed to switch autostart');
      })
      .finally(() => {
        setAutostartLoading(false);
      });
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

          {state && state !== 'notInstall' && (
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
              disabled={isAutostart}
            >
              <Switch
                checked={isAutostart}
                loading={autostartLoading}
                size="small"
                title={t('settings.netbird.autostart')}
              />
            </Popconfirm>
          )}
        </div>

        <div className="flex items-center space-x-2">
          {state && ['notRunning', 'notLogin', 'stopped', 'running'].includes(state) && (
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
                description={t('settings.netbird.stopDesc')}
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

              {/* uninstall */}
              <Popover
                content={<Uninstall onSuccess={onSuccess} />}
                placement="bottomRight"
                arrow={false}
              >
                <div className="flex cursor-pointer rounded p-1 text-neutral-300 hover:bg-neutral-600">
                  <EllipsisIcon size={18} />
                </div>
              </Popover>
            </>
          )}
        </div>
      </div>

      {errMsg && <ErrorHelp error={errMsg} onRefresh={onSuccess} />}
    </>
  );
};
