#define MyAppName "ReEnvision AI"
#if GetEnv("PKG_VERSION") != ""
  #define MyAppVersion GetEnv("PKG_VERSION")
#else
  #define MyAppVersion "0.0.0"
#endif
#if GetEnv("HF_TOKEN") != ""
  #define HfToken GetEnv("HF_TOKEN")
#else
  #define HfToken "NO_TOKEN_SPECIFIED"
#endif
#define MyAppPublisher "ReEnvision AI LLC"
#define MyAppURL "https://reenvision.ai/"
#define MyAppExeName "ReEnvisionAI.exe"
#define MyIcon ".\assets\reai.ico"

[Setup]
AppId={{eaf7e858-2cbc-4631-84f0-2d7074fc7027}}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
VersionInfoVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=admin
OutputBaseFilename=ReEnvisionAISetup
SetupIconFile={#MyIcon}
UninstallDisplayIcon={uninstallexe}
Compression=lzma2
SolidCompression=no
WizardStyle=modern
ChangesEnvironment=yes
OutputDir=..\dist\

SetupLogging=yes
CloseApplications=yes
RestartApplications=no
RestartIfNeededByRun=yes

;WizardSmallImageFile=.\assets\setup.bmp

MinVersion=10.0.19045

DirExistsWarning=no
UsedUserAreasWarning=no
DisableDirPage=yes
DisableFinishedPage=yes
DisableReadyMemo=yes
DisableReadyPage=yes
DisableStartupPrompt=yes
DisableWelcomePage=yes

SetupMutex=Global\ReEnvisionSetupMutex

; Signing requires the "MsSign" tool configured in Inno Setup (local dev
; machines). CI passes /DSKIP_SIGNING to build unsigned artifacts until a
; code-signing certificate is available as a repository secret.
#ifndef SKIP_SIGNING
SignTool=MsSign $f
SignedUninstaller=yes
#endif

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[LangOptions]
DialogFontSize=12

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{commondesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{commonprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{commonstartmenu}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{commonstartup}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"

[Files]
Source: "..\dist\windows\ReEnvisionAI.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}"; Flags: signonce ignoreversion 64bit
Source: "..\dist\windows\podman-5.4.1-setup.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall; Check: NotAnUpdate
Source: "..\dist\windows\config.json"; DestDir: "{localappdata}\ReEnvisionAI\"; Flags: ignoreversion
Source: "..\dist\windows\.wslconfig"; DestDir: "{%USERPROFILE}"; Flags: ignoreversion
Source: "..\dist\windows\reai_welcome.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\dist\windows\wsl_install.ps1"; DestDir: "{tmp}"; Attribs: hidden; Flags: signonce ignoreversion deleteafterinstall; Check: NotAnUpdate
Source: "..\dist\windows\post_wsl_install.ps1"; DestDir: "{app}"; Attribs: hidden; Permissions: users-modify; Flags: ignoreversion signonce; Check: NotAnUpdate
Source: "..\dist\windows\podman_setup.bat"; DestDir: "{app}"; Attribs: hidden; Flags: ignoreversion; Check: NotAnUpdate



[Run]
Filename: "Powershell.exe"; Parameters: "-ExecutionPolicy Bypass -File '{tmp}\wsl_install.ps1'"; Flags: shellexec waituntilterminated; StatusMsg: "Installing Windows Subsystem for Linux, please wait.."; Check: NotAnUpdate

Filename: "{tmp}\podman-5.4.1-setup.exe"; Parameters: "/quiet"; Flags: shellexec  waituntilterminated; StatusMsg: "Installing Podman, please wait..."; BeforeInstall: SetMarqueeProgress(True); Check: NotAnUpdate

; Only bake in a Hugging Face credential when a token was actually supplied at
; build time. A shared build-time token is extractable from every installer and
; exposed here on the cmdkey command line (audit finding): the goal is to stop
; shipping a shared token and move to per-user HF auth. A tokenless build now
; installs no credential at all instead of baking in "NO_TOKEN_SPECIFIED".
#if GetEnv("HF_TOKEN") != ""
Filename: "{cmd}"; Parameters: "/c cmdkey /generic:ReEnvisionAI/hf_token /user:reai /pass:{#HfToken}"; Flags: runhidden shellexec waituntilterminated
#endif

Filename: "netsh"; Parameters: "advfirewall firewall delete rule name=""ReEnvision AI"""; Flags:  waituntilterminated; Check: NotAnUpdate
Filename: "netsh"; Parameters: "advfirewall firewall add rule name=""ReEnvision AI"" dir=in action=allow protocol=TCP localport={code:GetPort}"; Flags:  waituntilterminated; StatusMsg: "Setting up firewall rule, please wait..."; AfterInstall: SetMarqueeProgress(False); Check: NotAnUpdate

[Registry]
Root: HKLM; Subkey: "SOFTWARE\ReEnvisionAI\ReEnvisionAI"; ValueType: dword; ValueName: "Port"; ValueData: "{code:GetPort}"; Flags: uninsdeletekey; Check: NotAnUpdate
Root: HKLM; Subkey: "SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce"; ValueType: string; ValueName: "ReEnvisionAI_Setup"; ValueData: "cmd.exe /c ""{app}\podman_setup.bat"""; Flags: uninsdeletevalue; Check: NotAnUpdate

[UninstallDelete]
Type: filesandordirs; Name: "{%LOCALAPPDATA}\ReEnvision*"
Type: files; Name: "{%USERPROFILE}\.wslconfig"


[UninstallRun]
Filename: "taskkill"; Parameters: "/im ""{#MyAppExeName}"" /f /t"; Flags: runhidden; RunOnceId: "stopReEnvisionApp"
Filename: "netsh"; Parameters: "advfirewall firewall delete rule name=""ReEnvision AI"""; Flags: waituntilterminated; RunOnceId: "DeleteReEnvisionAIFirewallRule"

Filename: "{cmd}"; Parameters: "/c timeout 5"; Flags: runhidden; RunOnceId: "Pause"

[Messages]
WizardReady=ReEnvision AI
ReadyLabel1=%n Let's get you up and running on the world's first distributed large language model.
SetupAppRunningError=Another ReEnvision installer is running.%n%nPlease cancel or finish the other installer, then click OK to continue with this installer, or Cancel to exit.

[Code]
var
  RandomPort: Integer;
  IsUpdate: Boolean;
  
procedure SetMarqueeProgress(Marquee: Boolean);
begin
  if Marquee then
  begin
    WizardForm.ProgressGauge.Style := npbstMarquee;
  end
    else
  begin
    WizardForm.ProgressGauge.Style := npbstNormal;
  end;
end;

function GetPort(Param: String): String;
begin
  Result := IntToStr(RandomPort);
end;

function InitializeSetup: Boolean;
begin
  RandomPort := 31330 + Random(52000 - 31330 + 1);
  Result:= True;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = wpInstalling then
    Log(ExpandConstant('Looking for {autopf}\{#MyAppName}\{#MyAppExeName}'));
    IsUpdate := FileExists(ExpandConstant('{autopf}\{#MyAppName}\{#MyAppExeName}'));

end;

function NotAnUpdate: Boolean;
begin
  Result := not IsUpdate;
 end;