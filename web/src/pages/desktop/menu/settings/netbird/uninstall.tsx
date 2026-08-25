import { useState } from 'react';
import { Modal } from 'antd';
import { Trash2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import * as api from '@/api/extensions/netbird.ts';

type UninstallProps = {
  onSuccess: () => void;
};

export const Uninstall = ({ onSuccess }: UninstallProps) => {
  const { t } = useTranslation();

  const [isLoading, setIsLoading] = useState(false);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [errMsg, setErrMsg] = useState('');

  function uninstall() {
    if (isLoading) return;
    setIsLoading(true);

    api
      .uninstall()
      .then((rsp) => {
        if (rsp.code !== 0) {
          setErrMsg(rsp.msg);
          return;
        }

        setIsModalOpen(false);
      })
      .catch((err) => {
        setErrMsg(err.message || 'Uninstall failed');
      })
      .finally(() => {
        setIsLoading(false);
        onSuccess();
      });
  }

  const title = (
    <div className="flex items-center space-x-1 text-red-500">
      <Trash2Icon size={18} />
      <span>{t('settings.netbird.uninstall')}</span>
    </div>
  );

  return (
    <>
      <div
        className="flex h-[30px] cursor-pointer items-center space-x-1 rounded px-2 py-1 text-neutral-300 hover:bg-neutral-700/70"
        onClick={() => setIsModalOpen(true)}
      >
        <span>{t('settings.netbird.uninstall')}</span>
      </div>

      <Modal
        title={title}
        open={isModalOpen}
        centered={true}
        okType="danger"
        okText={t('settings.netbird.okBtn')}
        cancelText={t('settings.netbird.cancelBtn')}
        onOk={uninstall}
        onCancel={() => setIsModalOpen(false)}
        confirmLoading={isLoading}
      >
        <div className="py-5">
          <p className="text-base">{t('settings.netbird.uninstallDesc')}</p>
          {errMsg && <p className="pt-3 text-sm text-red-500">{errMsg}</p>}
        </div>
      </Modal>
    </>
  );
};
