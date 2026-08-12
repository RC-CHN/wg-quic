import path from 'node:path';

export const managementServiceComponentId = 'WgQuicManagementService';

const securedBinDirectoryId = 'WgQuicSecuredBinDirectory';
const installedDirectorySDDL =
  'O:SYG:SYD:PAI' +
  '(A;OICI;FA;;;SY)' +
  '(A;OICI;FA;;;BA)' +
  '(A;OICI;GRGX;;;BU)';

function escapeXmlAttribute(value) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;');
}

export function renderManagementServiceFragment(sourcePath) {
  if (!path.isAbsolute(sourcePath)) {
    throw new Error('the management service source path must be absolute');
  }

  const escapedSource = escapeXmlAttribute(sourcePath);
  return `<?xml version="1.0" encoding="UTF-8"?>
<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
  <Fragment>
    <DirectoryRef Id="INSTALLDIR">
      <Directory Id="${securedBinDirectoryId}" Name="bin" />
      <Component Id="${managementServiceComponentId}" Guid="*" Win64="yes">
        <CreateFolder>
          <PermissionEx Id="WgQuicInstallRootPermissions" Sddl="${installedDirectorySDDL}" />
        </CreateFolder>
        <CreateFolder Directory="${securedBinDirectoryId}">
          <PermissionEx Id="WgQuicInstallBinPermissions" Sddl="${installedDirectorySDDL}" />
        </CreateFolder>
        <File Id="WgQuicManagementServiceExecutable" Source="${escapedSource}" Name="wg-quic-manager.exe" KeyPath="yes" />
        <ServiceInstall
          Id="WgQuicManagementServiceInstall"
          Name="wg-quic-manager"
          DisplayName="wg-quic Manager"
          Description="Provides privileged tunnel management for the wg-quic desktop application."
          Type="ownProcess"
          Start="auto"
          ErrorControl="normal"
          Account="LocalSystem"
          Arguments="broker-service"
        />
        <ServiceControl
          Id="WgQuicManagementServiceControl"
          Name="wg-quic-manager"
          Start="install"
          Stop="both"
          Remove="uninstall"
          Wait="yes"
        />
      </Component>
    </DirectoryRef>
  </Fragment>
</Wix>
`;
}
