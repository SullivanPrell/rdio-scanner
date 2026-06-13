/*
 * *****************************************************************************
 * Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>
 * ****************************************************************************
 */

import { HttpClient, HttpErrorResponse, HttpHeaders } from '@angular/common/http';
import { EventEmitter, Injectable, OnDestroy } from '@angular/core';
import { AbstractControl, FormBuilder, FormGroup, ValidationErrors, ValidatorFn, Validators } from '@angular/forms';
import { MatSnackBar } from '@angular/material/snack-bar';
import { firstValueFrom, timer } from 'rxjs';
import { AppUpdateService } from '../../../shared/update/update.service';

export interface Access {
    id?: string;
    code?: string;
    expiration?: Date;
    ident?: string;
    limit?: number;
    order?: number;
    systems?: {
        id: number;
        talkgroups: {
            id: number;
        }[] | number[] | '*';
    }[] | number[] | '*';
}

export interface Alert {
    begin: number;
    end: number;
    frequency: number;
    type: OscillatorType;
}

export interface Alerts {
    [key: string]: Alert[];
}

export interface AdminEvent {
    authenticated?: boolean;
    config?: Config;
    docker?: boolean;
    passwordNeedChange?: boolean;
}

export interface Apikey {
    id?: string;
    disabled?: boolean;
    ident?: string;
    key?: string;
    order?: number;
    systems?: {
        id: number;
        talkgroups: number[] | '*';
    }[] | number[] | '*';
}

export interface Config {
    access?: Access[];
    apikeys?: Apikey[];
    dirwatch?: Dirwatch[];
    downstreams?: Downstream[];
    groups?: Group[];
    options?: Options;
    systems?: System[];
    tags?: Tag[];
    version?: string;
}

export interface Dirwatch {
    id?: string;
    delay?: number;
    deleteAfter?: boolean;
    directory?: string;
    disabled?: boolean;
    extension?: string;
    frequency?: number;
    mask?: string;
    order?: number;
    siteId?: number;
    systemId?: number;
    talkgroupId?: number;
    type?: string;
}

export interface Downstream {
    id?: string;
    apikey?: string;
    disabled?: boolean;
    order?: number;
    systems?: {
        id?: number;
        id_as?: number;
        talkgroups?: {
            id: number;
            id_as?: number;
        }[] | number[] | '*';
    }[] | number[] | '*';
    url?: string;
}

export interface Group {
    id?: number;
    alert?: string;
    label?: string;
    led?: string;
    order?: number;
}

export interface Log {
    id?: number;
    dateTime: Date;
    level: number;
    message: string;
}

export interface LogsQuery {
    count: number;
    dateStart: Date;
    dateStop: Date;
    options: LogsQueryOptions;
    logs: Log[];
}

export interface LogsQueryOptions {
    date?: Date;
    level?: 'error' | 'info' | 'warn';
    limit: number;
    offset: number;
    sort: number;
}

export interface BridgeChannelConfig {
    channelIndex?: number;
    deviceSetIndex?: number;
    frequencyHz?: number;
    label?: string;
    protocol?: 'nfm' | 'dsd' | 'nxdn';
    sampleRate?: number;
    systemRef?: number;
    talkgroupRef?: number;
    udpPort?: number;
}

export interface SDRangelChannel {
    index?: number;
    idText?: string;
    title?: string;
    direction?: number;
}

export interface SDRangelDeviceSet {
    samplingDeviceIndex?: number;
    samplingDeviceHwType?: string;
    channels?: SDRangelChannel[];
}

export interface SDRangelStatus {
    connected: boolean;
    version?: string;
    os?: string;
    deviceSets?: SDRangelDeviceSet[];
}

export interface SDRangelDeviceSetConfig {
    index: number;
    hwType: string;
    sequence: number;
    centerFrequencyHz: number;
    sampleRateHz: number;
}

export interface SDRangelProvisionResult {
    success: boolean;
    messages: string[];
}

export interface SDRangelServiceStatus {
    running: boolean;
    mode: 'docker' | 'native' | 'unavailable';
    message?: string;
    containerId?: string;
    containerName?: string;
    pid?: number;
    startedAt?: string;
    uptimeSeconds?: number;
}

export interface SDRangelServiceResult {
    success: boolean;
    message: string;
}

export interface ImportResult {
    systems?: System[];
    groups?: Group[];
    tags?: Tag[];
    channels?: BridgeChannelConfig[];
}

export interface RRSnapshotInfo {
    stateId: number;
    stateName: string;
    stateAbbr: string;
    downloadedAt: string;
    countyCount: number;
}

export interface RRDownloadJobStatus {
    done: boolean;
    error?: string;
    progress: number;
    countyTotal: number;
    countyDone: number;
    stateId: number;
}

export interface RRState {
    id: number;
    name: string;
    abbr: string;
}

export interface RRCounty {
    id: number;
    name: string;
}

export interface Options {
    audioConversion?: 0 | 1 | 2 | 3;
    autoPopulate?: boolean;
    bridgeChannels?: BridgeChannelConfig[];
    bridgeEnabled?: boolean;
    bridgeHost?: string;
    bridgePort?: number;
    branding?: string;
    dimmerDelay?: number;
    disableDuplicateDetection?: boolean;
    duplicateDetectionTimeFrame?: number;
    email?: string;
    keypadBeeps?: string;
    maxClients?: number;
    playbackGoesLive?: boolean;
    pruneDays?: number;
    sdrangelBinaryPath?: string;
    sdrangelContainerName?: string;
    showListenersCount?: boolean;
    sortTalkgroups?: boolean;
    time12hFormat?: boolean;
}

export interface Site {
    id?: number | null;
    label?: string;
    order?: number;
    siteRef?: number;
}

export interface System {
    id?: number | null;
    alert?: string;
    autoPopulate?: boolean;
    blacklists?: string;
    delay?: number;
    label?: string;
    led?: string | null;
    order?: number | null;
    sites?: Site[];
    systemRef?: number;
    talkgroups?: Talkgroup[];
    type?: string;
    units?: Unit[];
}

export interface Tag {
    id?: number;
    alert?: string;
    label?: string;
    led?: string;
    order?: number;
}

export interface Talkgroup {
    id?: number | null;
    alert?: string;
    delay?: number;
    frequency?: number | null;
    groupIds?: number[];
    label?: string;
    led?: string | null;
    name?: string;
    order?: number;
    tagId?: number;
    talkgroupRef?: number;
    type?: string;
}

export interface Unit {
    id?: number | null;
    label?: string;
    order?: number;
    unitRef?: number;
    unitFrom?: number;
    unitTo?: number;
}

enum url {
    alerts = 'alerts',
    config = 'config',
    login = 'login',
    logout = 'logout',
    logs = 'logs',
    password = 'password',
    sdrangelStatus = 'sdrangel/status',
    sdrangelProvision = 'sdrangel/provision',
    sdrangelService = 'sdrangel/service',
    sdrangelServiceAction = 'sdrangel/service/action',
    sdrangelServiceLogs = 'sdrangel/service/logs',
    importFRS = 'import/frs',
    importGMRS = 'import/gmrs',
    importChirp = 'import/chirp',
    importRRCSV = 'import/rr-csv',
    importTRSCSV = 'import/trs-csv',
    importRRStates = 'import/rr-states',
    importRRCounties = 'import/rr-counties',
    importRRCounty = 'import/rr-county',
    rrStateDownload = 'import/rr-state-download',
    rrSnapshots = 'import/rr-snapshots',
    rrSnapshotCounties = 'import/rr-snapshot-counties',
    rrSnapshotCounty = 'import/rr-snapshot-county',
}

const SESSION_STORAGE_KEY = 'rdio-scanner-admin-token';

declare global {
    interface Window {
        webkitAudioContext: typeof AudioContext;
    }
}

@Injectable()
export class RdioScannerAdminService implements OnDestroy {
    Alerts: Alerts | undefined;

    event = new EventEmitter<AdminEvent>();

    get authenticated() {
        return !!this.token;
    }

    get docker() {
        return this._docker;
    }

    get passwordNeedChange() {
        return this._passwordNeedChange;
    }

    private audioContext: AudioContext | undefined;

    private configWebSocket: WebSocket | undefined;

    private _docker = false;

    private _passwordNeedChange = false;

    private get token(): string {
        return window?.sessionStorage?.getItem(SESSION_STORAGE_KEY) || '';
    }

    private set token(token: string) {
        window?.sessionStorage?.setItem(SESSION_STORAGE_KEY, token);
    }

    constructor(
        appUpdateService: AppUpdateService,
        private matSnackBar: MatSnackBar,
        private ngFormBuilder: FormBuilder,
        private ngHttpClient: HttpClient,
    ) {
        this.configWebSocketOpen();
    }

    ngOnDestroy(): void {
        this.event.complete();

        this.configWebSocketClose();
    }

    async changePassword(currentPassword: string, newPassword: string): Promise<void> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.post<{ passwordNeedChange: boolean }>(
                this.getUrl(url.password),
                { currentPassword, newPassword },
                { headers: this.getHeaders(), responseType: 'json' },
            ));

            this._passwordNeedChange = res.passwordNeedChange;

            this.event.next({ passwordNeedChange: this.passwordNeedChange });

        } catch (error) {
            this.errorHandler(error);

            throw error;

        }
    }

    async getConfig(): Promise<Config> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.get<{
                config: Config;
                docker: boolean;
                passwordNeedChange: boolean;
            }>(
                this.getUrl(url.config),
                { headers: this.getHeaders(), responseType: 'json' },
            ));

            if (res.docker !== this._docker) {
                this._docker = res.docker;

                this.event.emit({ docker: this.docker })
            }

            if (res.passwordNeedChange !== this._passwordNeedChange) {
                this._passwordNeedChange = res.passwordNeedChange;

                this.event.emit({ passwordNeedChange: this.passwordNeedChange });
            }

            return res.config;

        } catch (error) {
            this.errorHandler(error);
        }

        return {};
    }

    getLeds(): string[] {
        return ['blue', 'cyan', 'green', 'magenta', 'orange', 'red', 'white', 'yellow'];
    }

    async getLogs(options: LogsQueryOptions): Promise<LogsQuery | undefined> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.post<LogsQuery>(
                this.getUrl(url.logs),
                options,
                { headers: this.getHeaders(), responseType: 'json' },
            ));

            return res;

        } catch (error) {
            this.errorHandler(error);

            return undefined;
        }
    }

    async loadAlerts(): Promise<void> {
        try {
            this.Alerts = await firstValueFrom(this.ngHttpClient.get<Alerts>(
                this.getUrl(url.alerts),
                { headers: this.getHeaders(), responseType: 'json' },
            ));


        } catch (error) {
            this.errorHandler(error);
        }
    }

    async login(password: string): Promise<boolean> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.post<{
                passwordNeedChange: boolean,
                token: string
            }>(
                this.getUrl(url.login),
                { password },
                { headers: this.getHeaders(), responseType: 'json' },
            ));

            this.token = res.token;

            this._passwordNeedChange = res.passwordNeedChange;

            this.event.emit({
                authenticated: this.authenticated,
                passwordNeedChange: res.passwordNeedChange,
            });

            this.configWebSocketOpen();

            return !!this.token;

        } catch (error) {
            this.errorHandler(error);

            return false;
        }
    }

    async logout(): Promise<boolean> {
        try {
            this.ngHttpClient.post(
                this.getUrl(url.logout),
                null,
                { headers: this.getHeaders(), responseType: 'text' },
            );

            this.configWebSocketClose();

            this.token = '';

            this.event.emit({ authenticated: this.authenticated });

            return true;

        } catch (error) {
            this.errorHandler(error);

            return false;
        }
    }

    async playAlert(name: string): Promise<void> {
        return new Promise((resolve) => {
            if (this.audioContext === undefined) {
                this.audioContext = new (window.AudioContext || window.webkitAudioContext)({ latencyHint: 'playback' });
            }

            if (this.Alerts !== undefined && name in this.Alerts) {
                const ctx = this.audioContext;

                const seq = this.Alerts[name];

                const gn = ctx.createGain();

                gn.gain.value = .1;

                gn.connect(ctx.destination);

                seq.forEach((beep, index) => {
                    const osc = ctx.createOscillator();

                    osc.connect(gn);

                    osc.frequency.value = beep.frequency;

                    osc.type = beep.type;

                    if (index === seq.length - 1) {
                        osc.onended = () => resolve();
                    }

                    osc.start(ctx.currentTime + beep.begin);

                    osc.stop(ctx.currentTime + beep.end);
                });

            } else {
                resolve();
            }
        });
    }

    async saveConfig(config: Config): Promise<Config> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.put<{ config: Config }>(
                this.getUrl(url.config),
                config,
                { headers: this.getHeaders(), responseType: 'json' },
            ));

            return res.config;

        } catch (error) {
            this.errorHandler(error);

            return config;
        }
    }

    async getSDRangelStatus(): Promise<SDRangelStatus> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<SDRangelStatus>(
                this.getUrl(url.sdrangelStatus),
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return { connected: false };
        }
    }

    async provisionSDRangel(deviceSets: SDRangelDeviceSetConfig[]): Promise<SDRangelProvisionResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<SDRangelProvisionResult>(
                this.getUrl(url.sdrangelProvision),
                { deviceSets },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return { success: false, messages: [String(error)] };
        }
    }

    async getSDRangelServiceStatus(): Promise<SDRangelServiceStatus> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<SDRangelServiceStatus>(
                this.getUrl(url.sdrangelService),
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return { running: false, mode: 'unavailable', message: String(error) };
        }
    }

    async controlSDRangelService(action: 'start' | 'stop' | 'restart', binaryPath?: string, args?: string): Promise<SDRangelServiceResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<SDRangelServiceResult>(
                this.getUrl(url.sdrangelServiceAction),
                { action, binaryPath: binaryPath ?? '', args: args ?? '' },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return { success: false, message: String(error) };
        }
    }

    async getSDRangelServiceLogs(): Promise<string[]> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<string[]>(
                this.getUrl(url.sdrangelServiceLogs),
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return [];
        }
    }

    async importFRS(systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importFRS),
                { systemRef, portBase },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async importGMRS(systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importGMRS),
                { systemRef, portBase },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async importChirp(file: File, systemLabel: string, systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            const form = new FormData();
            form.append('file', file);
            form.append('systemLabel', systemLabel);
            form.append('systemRef', String(systemRef));
            form.append('portBase', String(portBase));
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importChirp),
                form,
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async importTRSCSV(file: File, systemLabel: string, systemRef: number): Promise<ImportResult> {
        try {
            const form = new FormData();
            form.append('file', file);
            form.append('systemLabel', systemLabel);
            form.append('systemRef', String(systemRef));
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importTRSCSV),
                form,
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async importRRCSV(file: File, systemLabel: string, systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            const form = new FormData();
            form.append('file', file);
            form.append('systemLabel', systemLabel);
            form.append('systemRef', String(systemRef));
            form.append('portBase', String(portBase));
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importRRCSV),
                form,
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async importRRStates(username: string, password: string, appKey: string): Promise<RRState[]> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<RRState[]>(
                this.getUrl(url.importRRStates),
                { username, password, appKey },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return [];
        }
    }

    async importRRCounties(username: string, password: string, appKey: string, stateId: number): Promise<RRCounty[]> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<RRCounty[]>(
                this.getUrl(url.importRRCounties),
                { username, password, appKey, stateId },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return [];
        }
    }

    async importRRCounty(username: string, password: string, appKey: string, countyId: number, systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.importRRCounty),
                { username, password, appKey, countyId, systemRef, portBase },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async startRRStateDownload(username: string, password: string, appKey: string, stateId: number, stateName: string, stateAbbr: string): Promise<string> {
        try {
            const res = await firstValueFrom(this.ngHttpClient.post<{ jobId: string }>(
                this.getUrl(url.rrStateDownload),
                { username, password, appKey, stateId, stateName, stateAbbr },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
            return res.jobId;
        } catch (error) {
            this.errorHandler(error);
            return '';
        }
    }

    async getRRDownloadJobStatus(jobId: string): Promise<RRDownloadJobStatus> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<RRDownloadJobStatus>(
                this.getUrl(url.rrStateDownload) + `?jobId=${jobId}`,
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return { done: true, error: String(error), progress: 0, countyTotal: 0, countyDone: 0, stateId: 0 };
        }
    }

    async listRRSnapshots(): Promise<RRSnapshotInfo[]> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<RRSnapshotInfo[]>(
                this.getUrl(url.rrSnapshots),
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return [];
        }
    }

    async getRRSnapshotCounties(stateId: number): Promise<RRCounty[]> {
        try {
            return await firstValueFrom(this.ngHttpClient.get<RRCounty[]>(
                this.getUrl(url.rrSnapshotCounties) + `?stateId=${stateId}`,
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return [];
        }
    }

    async importRRSnapshotCounty(stateId: number, countyId: number, systemRef: number, portBase: number): Promise<ImportResult> {
        try {
            return await firstValueFrom(this.ngHttpClient.post<ImportResult>(
                this.getUrl(url.rrSnapshotCounty),
                { stateId, countyId, systemRef, portBase },
                { headers: this.getHeaders(), responseType: 'json' },
            ));
        } catch (error) {
            this.errorHandler(error);
            return {};
        }
    }

    async deleteRRSnapshot(stateId: number): Promise<void> {
        try {
            await firstValueFrom(this.ngHttpClient.delete(
                this.getUrl(url.rrSnapshots),
                { headers: this.getHeaders(), body: { stateId } },
            ));
        } catch (error) {
            this.errorHandler(error);
        }
    }

    newAccessForm(access?: Access): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.nonNullable.control(access?.id),
            code: this.ngFormBuilder.nonNullable.control(access?.code, [Validators.required, this.validateAccessCode()]),
            expiration: this.ngFormBuilder.nonNullable.control(access?.expiration),
            ident: this.ngFormBuilder.nonNullable.control(access?.ident, Validators.required),
            limit: this.ngFormBuilder.nonNullable.control(access?.limit),
            order: this.ngFormBuilder.nonNullable.control(access?.order),
            systems: this.ngFormBuilder.nonNullable.control(access?.systems),
        });
    }

    newApikeyForm(apikey?: Apikey): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(apikey?.id),
            disabled: this.ngFormBuilder.control(apikey?.disabled),
            ident: this.ngFormBuilder.control(apikey?.ident, Validators.required),
            key: this.ngFormBuilder.control(apikey?.key, [Validators.required, this.validateApikey()]),
            order: this.ngFormBuilder.control(apikey?.order),
            systems: this.ngFormBuilder.control(apikey?.systems, Validators.required),
        });
    }

    newConfigForm(config?: Config): FormGroup {
        return this.ngFormBuilder.group({
            access: this.ngFormBuilder.array(config?.access?.map((access) => this.newAccessForm(access)) || []),
            apikeys: this.ngFormBuilder.array(config?.apikeys?.map((apikey) => this.newApikeyForm(apikey)) || []),
            dirwatch: this.ngFormBuilder.array(config?.dirwatch?.map((dirwatch) => this.newDirwatchForm(dirwatch)) || []),
            downstreams: this.ngFormBuilder.array(config?.downstreams?.map((downstream) => this.newDownstreamForm(downstream)) || []),
            groups: this.ngFormBuilder.array(config?.groups?.map((group) => this.newGroupForm(group)) || []),
            options: this.newOptionsForm(config?.options),
            systems: this.ngFormBuilder.array(config?.systems?.map((system) => this.newSystemForm(system)) || []),
            tags: this.ngFormBuilder.array(config?.tags?.map((tag) => this.newTagForm(tag)) || []),
            version: this.ngFormBuilder.control(config?.version),
        });
    }

    newDirwatchForm(dirwatch?: Dirwatch): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(dirwatch?.id),
            delay: this.ngFormBuilder.control(typeof dirwatch?.delay === 'number' ? Math.max(2000, dirwatch?.delay) : 2000),
            deleteAfter: this.ngFormBuilder.control(dirwatch?.deleteAfter),
            directory: this.ngFormBuilder.control(dirwatch?.directory, [Validators.required, this.validateDirectory()]),
            disabled: this.ngFormBuilder.control(dirwatch?.disabled),
            extension: this.ngFormBuilder.control(dirwatch?.extension, this.validateExtension()),
            frequency: this.ngFormBuilder.control(dirwatch?.frequency, Validators.min(1)),
            mask: this.ngFormBuilder.control(dirwatch?.mask, this.validateMask()),
            order: this.ngFormBuilder.control(dirwatch?.order),
            siteId: this.ngFormBuilder.control(dirwatch?.siteId),
            systemId: this.ngFormBuilder.control(dirwatch?.systemId, this.validateDirwatchSystemId()),
            talkgroupId: this.ngFormBuilder.control(dirwatch?.talkgroupId, this.validateDirwatchTalkgroupId()),
            type: this.ngFormBuilder.control(dirwatch?.type ?? 'default'),
        });
    }

    newDownstreamForm(downstream?: Downstream): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(downstream?.id),
            apikey: this.ngFormBuilder.control(downstream?.apikey, [Validators.required, this.validateApikey()]),
            disabled: this.ngFormBuilder.control(downstream?.disabled),
            order: this.ngFormBuilder.control(downstream?.order),
            systems: this.ngFormBuilder.control(downstream?.systems, Validators.required),
            url: this.ngFormBuilder.control(downstream?.url, [Validators.required, this.validateUrl(), this.validateDownstreamUrl()]),
        });
    }

    newGroupForm(group?: Group): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(group?.id),
            alert: this.ngFormBuilder.control(group?.alert || ''),
            label: this.ngFormBuilder.control(group?.label, Validators.required),
            led: this.ngFormBuilder.control(group?.led || ''),
            order: this.ngFormBuilder.control(group?.order),
        });
    }

    newBridgeChannelConfigForm(cfg?: BridgeChannelConfig): FormGroup {
        return this.ngFormBuilder.group({
            channelIndex: this.ngFormBuilder.control(cfg?.channelIndex ?? 0, [Validators.required, Validators.min(0)]),
            deviceSetIndex: this.ngFormBuilder.control(cfg?.deviceSetIndex ?? 0, [Validators.required, Validators.min(0)]),
            frequencyHz: this.ngFormBuilder.control(cfg?.frequencyHz ?? 0, Validators.min(0)),
            label: this.ngFormBuilder.control(cfg?.label ?? '', Validators.required),
            protocol: this.ngFormBuilder.control(cfg?.protocol ?? 'nfm'),
            sampleRate: this.ngFormBuilder.control(cfg?.sampleRate ?? 8000, [Validators.required, Validators.min(1)]),
            systemRef: this.ngFormBuilder.control(cfg?.systemRef ?? 1, [Validators.required, Validators.min(1)]),
            talkgroupRef: this.ngFormBuilder.control(cfg?.talkgroupRef ?? 1, [Validators.required, Validators.min(1)]),
            udpPort: this.ngFormBuilder.control(cfg?.udpPort ?? 9000, [Validators.required, Validators.min(1), Validators.max(65535)]),
        });
    }

    newOptionsForm(options?: Options): FormGroup {
        return this.ngFormBuilder.group({
            audioConversion: this.ngFormBuilder.control(options?.audioConversion),
            autoPopulate: this.ngFormBuilder.control(options?.autoPopulate),
            bridgeChannels: this.ngFormBuilder.array(options?.bridgeChannels?.map((cfg) => this.newBridgeChannelConfigForm(cfg)) ?? []),
            bridgeEnabled: this.ngFormBuilder.control(options?.bridgeEnabled ?? false),
            bridgeHost: this.ngFormBuilder.control(options?.bridgeHost ?? '127.0.0.1'),
            bridgePort: this.ngFormBuilder.control(options?.bridgePort ?? 8091, [Validators.required, Validators.min(1), Validators.max(65535)]),
            branding: this.ngFormBuilder.control(options?.branding),
            dimmerDelay: this.ngFormBuilder.control(options?.dimmerDelay, [Validators.required, Validators.min(0)]),
            disableDuplicateDetection: this.ngFormBuilder.control(options?.disableDuplicateDetection),
            duplicateDetectionTimeFrame: this.ngFormBuilder.control(options?.duplicateDetectionTimeFrame, [Validators.required, Validators.min(0)]),
            email: this.ngFormBuilder.control(options?.email),
            keypadBeeps: this.ngFormBuilder.control(options?.keypadBeeps, Validators.required),
            maxClients: this.ngFormBuilder.control(options?.maxClients, [Validators.required, Validators.min(1)]),
            playbackGoesLive: this.ngFormBuilder.control(options?.playbackGoesLive),
            pruneDays: this.ngFormBuilder.control(options?.pruneDays, [Validators.required, Validators.min(0)]),
            sdrangelBinaryPath: this.ngFormBuilder.control(options?.sdrangelBinaryPath ?? ''),
            sdrangelContainerName: this.ngFormBuilder.control(options?.sdrangelContainerName ?? ''),
            showListenersCount: this.ngFormBuilder.control(options?.showListenersCount),
            sortTalkgroups: this.ngFormBuilder.control(options?.sortTalkgroups),
            time12hFormat: this.ngFormBuilder.control(options?.time12hFormat),
        });
    }

    newSiteForm(site?: Site): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(site?.id),
            label: this.ngFormBuilder.control(site?.label, Validators.required),
            order: this.ngFormBuilder.control(site?.order),
            siteRef: this.ngFormBuilder.control(site?.siteRef, [Validators.required, Validators.min(1), this.validateSiteRef()]),
        });
    }

    newSystemForm(system?: System): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(system?.id),
            alert: this.ngFormBuilder.control(system?.alert),
            autoPopulate: this.ngFormBuilder.control(system?.autoPopulate),
            blacklists: this.ngFormBuilder.control(system?.blacklists, this.validateBlacklists()),
            delay: this.ngFormBuilder.control(system?.delay),
            label: this.ngFormBuilder.control(system?.label, Validators.required),
            led: this.ngFormBuilder.control(system?.led || ''),
            order: this.ngFormBuilder.control(system?.order),
            sites: this.ngFormBuilder.array(system?.sites?.map((site) => this.newSiteForm(site)) || []),
            systemRef: this.ngFormBuilder.control(system?.systemRef, [Validators.required, Validators.min(1), this.validateSystemRef()]),
            talkgroups: this.ngFormBuilder.array(system?.talkgroups?.map((talkgroup) => this.newTalkgroupForm(talkgroup)) || []),
            type: this.ngFormBuilder.control(system?.type || ''),
            units: this.ngFormBuilder.array(system?.units?.map((unit) => this.newUnitForm(unit)) || []),
        });
    }

    newTagForm(tag?: Tag): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(tag?.id),
            alert: this.ngFormBuilder.control(tag?.alert || ''),
            label: this.ngFormBuilder.control(tag?.label, Validators.required),
            led: this.ngFormBuilder.control(tag?.led || ''),
            order: this.ngFormBuilder.control(tag?.order),
        });
    }

    newTalkgroupForm(talkgroup?: Talkgroup): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(talkgroup?.id),
            alert: this.ngFormBuilder.control(talkgroup?.alert),
            delay: this.ngFormBuilder.control(talkgroup?.delay),
            frequency: this.ngFormBuilder.control(talkgroup?.frequency, Validators.min(0)),
            groupIds: this.ngFormBuilder.control(talkgroup?.groupIds, [Validators.required, this.validateGroup()]),
            label: this.ngFormBuilder.control(talkgroup?.label, Validators.required),
            led: this.ngFormBuilder.control(talkgroup?.led || ''),
            name: this.ngFormBuilder.control(talkgroup?.name, Validators.required),
            order: this.ngFormBuilder.control(talkgroup?.order),
            tagId: this.ngFormBuilder.control(talkgroup?.tagId, [Validators.required, this.validateTag()]),
            talkgroupRef: this.ngFormBuilder.control(talkgroup?.talkgroupRef, [Validators.required, Validators.min(1), this.validateTalkgroupRef()]),
            type: this.ngFormBuilder.control(talkgroup?.type || ''),
        });
    }

    newUnitForm(unit?: Unit): FormGroup {
        return this.ngFormBuilder.group({
            id: this.ngFormBuilder.control(unit?.id),
            label: this.ngFormBuilder.control(unit?.label, Validators.required),
            order: this.ngFormBuilder.control(unit?.order),
            unitRef: this.ngFormBuilder.control(unit?.unitRef, [Validators.min(1), this.validateUnitRef()]),
            unitFrom: this.ngFormBuilder.control(unit?.unitFrom, [Validators.min(1), this.validateUnitFrom()]),
            unitTo: this.ngFormBuilder.control(unit?.unitTo, [Validators.min(1), this.validateUnitTo()])
        });
    }

    private configWebSocketClose(): void {
        if (this.configWebSocket instanceof WebSocket) {
            this.configWebSocket.onclose = null;
            this.configWebSocket.onmessage = null;
            this.configWebSocket.onopen = null;

            this.configWebSocket.close();

            this.configWebSocket = undefined;
        }
    }

    private configWebSocketReconnect(): void {
        this.configWebSocketClose();

        this.configWebSocketOpen();
    }

    private configWebSocketOpen(): void {
        if (!this.token) {
            return;
        }

        const webSocketUrl = new URL(this.getUrl(url.config), window.location.href).href.replace(/^http/, 'ws');

        this.configWebSocket = new WebSocket(webSocketUrl);

        this.configWebSocket.onclose = (ev: CloseEvent) => {
            if (ev.code === 1000) {
                this.token = '';

                this.event.emit({ authenticated: this.authenticated });
            } else {
                timer(2000).subscribe(() => this.configWebSocketReconnect());
            }
        };

        this.configWebSocket.onopen = () => {
            this.configWebSocket?.send(this.token);

            if (this.configWebSocket instanceof WebSocket) {
                this.configWebSocket.onmessage = async (ev: MessageEvent<string>) => {
                    try {
                        const msg = JSON.parse(ev.data);
                        if (msg?.event === 'config_changed') {
                            const config = await this.getConfig();
                            this.event.emit({ config });
                        }
                    } catch {
                        // ignore malformed messages
                    }
                };
            }
        }
    }

    private errorHandler(error: unknown): void {
        if (!(error instanceof HttpErrorResponse)) {
            return;
        }

        if (error.status === 401) {
            this.token = '';

            this.event.emit({ authenticated: this.authenticated });

            this.configWebSocketClose();

        } else {
            this.matSnackBar.open(error.message, '', { duration: 5000 });
        }
    }

    private getHeaders(): HttpHeaders {
        return new HttpHeaders({
            Authorization: this.token || '',
        });
    }

    private getUrl(path: string): string {
        return `${window.location.href}/../api/admin${path.charAt(0) === '/' ? path : `/${path}`}`;
    }

    private validateAccessCode(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            const access: Access[] = control.parent?.parent?.getRawValue() || [];

            const count = access.reduce((c, a) => c += a.code === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateApikey(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            const apikeys: Apikey[] = control.parent?.parent?.getRawValue() || [];

            const count = apikeys.reduce((c, a) => c += a.key === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateBlacklists(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            return typeof control.value === 'string' && control.value.length ? /^[0-9]+(,[0-9]+)*$/.test(control.value) ? null : { invalid: true } : null;
        };
    }

    private validateDirectory(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            if (control.value.startsWith('\\')) {
                return { network: true }
            }

            const dirwatch: Dirwatch[] = control.parent?.parent?.getRawValue() || [];

            const count = dirwatch.reduce((c, a) => c += a.directory === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateDirwatchSystemId(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            const dirwatch = control.parent?.getRawValue() || {};

            const mask = dirwatch.mask || '';

            const type = dirwatch.type;

            return ['dsdplus', 'sdr-trunk', 'trunk-recorder'].includes(type) || control.value !== null || /#SYS/.test(mask) ? null : { required: true };
        };
    }

    private validateDirwatchTalkgroupId(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            const dirwatch = control.parent?.getRawValue() || {};

            const mask = dirwatch.mask || '';

            const type = dirwatch.type;

            return ['dsdplus', 'sdr-trunk', 'trunk-recorder'].includes(type) || control.value !== null || /#TG/.test(mask) ? null : { required: true };
        };
    }

    private validateDownstreamUrl(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            const downstream: Downstream[] = control.parent?.parent?.getRawValue() || [];

            const count = downstream.reduce((c, a) => c += a.url === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateExtension(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            return /^[0-9a-zA-Z]+$/.test(control.value) ? null : { invalid: true };
        };
    }

    private validateGroup(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'number') {
                return null;
            }

            const groupIds = control.root.get('groups')?.value.map((group: Group) => group.id);

            return groupIds ? groupIds.includes(control.value) ? null : { required: true } : null;
        };
    }

    private validateMask(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string') {
                return null;
            }

            const masks = ['#DATE', '#GROUP', '#HZ', '#KHZ', '#MHZ', '#SITE', '#SITELBL', '#SYS', '#SYSLBL', '#TAG', '#TG', '#TGAFS', '#TGHZ', '#TGKHZ', '#TGLBL', '#TGMHZ', '#TIME', '#UNIT', '#UNITLBL', '#ZTIME'];

            const metas = control.value.match(/(#[A-Z]+)/g) || [] as string[];

            const count = metas.reduce((c, m) => {
                if (masks.includes(m)) {
                    c++;
                }

                return c;
            }, 0);

            return count ? null : { invalid: true };
        };
    }

    private validateSiteRef(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (control.value === null || typeof control.value !== 'number') {
                return null;
            }

            const sites: Site[] = control.parent?.parent?.getRawValue() || [];

            const count = sites.reduce((c, s) => c += s.siteRef === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateSystemRef(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (control.value === null || typeof control.value !== 'number') {
                return null;
            }

            const systems: System[] = control.parent?.parent?.getRawValue() || [];

            const count = systems.reduce((c, s) => c += control.value !== null && control.value > 0 && s.systemRef === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateTag(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'number') {
                return null;
            }

            const tagIds = control.root.get('tags')?.value.map((tag: Tag) => tag.id);

            return tagIds ? tagIds.includes(control.value) ? null : { required: true } : null;
        };
    }

    private validateTalkgroupRef(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (control.value === null || typeof control.value !== 'number') {
                return null;
            }

            const talkgroups: Talkgroup[] = control.parent?.parent?.getRawValue() || [];

            const count = talkgroups.reduce((c, t) => c += t.talkgroupRef === control.value ? 1 : 0, 0);

            return count > 1 ? { duplicate: true } : null;
        };
    }

    private validateUnitRef(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            const unitRef = control.parent?.get('unitRef')?.value;

            const unitFrom = control.parent?.get('unitFrom')?.value;

            const unitTo = control.parent?.get('unitTo')?.value;

            const units: Unit[] = control.parent?.parent?.getRawValue() || [];

            const count = units.reduce((c, u) => c += u.unitRef === unitRef ? 1 : 0, 0);

            return unitRef === null && (unitFrom === null || unitTo === null)
                ? { required: true }
                : count > 1
                    ? { duplicate: true }
                    : null;
        };
    }

    private validateUnitFrom(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            const unitFrom = control.value;

            const unitTo = control.parent?.get('unitTo')?.value;

            if (typeof unitFrom === 'number' && typeof unitTo === 'number' && unitFrom >= unitTo) {
                return { range: true };
            }

            setTimeout(() => {
                control.parent?.get('unitRef')?.updateValueAndValidity();
                control.parent?.get('unitTo')?.updateValueAndValidity();
            });

            return null;
        }
    }

    private validateUnitTo(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            const unitFrom = control.parent?.get('unitFrom')?.value;

            const unitTo = control.value;

            if (typeof unitFrom === 'number' && typeof unitTo === 'number' && unitFrom >= unitTo) {
                return { range: true };
            }

            setTimeout(() => {
                control.parent?.get('unitRef')?.updateValueAndValidity();
                control.parent?.get('unitFrom')?.updateValueAndValidity();
            });

            return null;
        }
    }

    private validateUrl(): ValidatorFn {
        return (control: AbstractControl): ValidationErrors | null => {
            if (typeof control.value !== 'string' || !control.value.length) {
                return null;
            }

            return /^https?:\/\/.+$/.test(control.value) ? null : { invalid: true }
        };
    }
}
