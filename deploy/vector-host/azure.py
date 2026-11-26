#!/usr/bin/env python3
"""Provision a zonal Azure index host using az; run only from an operator machine."""
import argparse
import base64
import gzip
import hashlib
import importlib.util
import io
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
SOURCE = Path('/opt/quivr-vector-host')
HOST_SCRIPT = 'deploy/vector-host/host.py'
SOURCES = ('deploy/infrastructure.py', 'deploy/infrastructure.json', 'deploy/report-resources.sh', HOST_SCRIPT)
APPLIED_INTENT = 'applied-intent'
APPLIED_TAG = 'quivr-vector-applied-intent'
HOST_FILES = ('override.json', *SOURCES, APPLIED_INTENT)
module_spec = importlib.util.spec_from_file_location('vector_host', Path(__file__).with_name('host.py'))
host = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(host)


class ProvisioningError(RuntimeError):
    """Only explicit operation context and an exception class may be printed."""


class ProvisioningContext:
    def __init__(self):
        self.at('validate local inputs', 'local inputs')

    def at(self, step, resource):
        self.step, self.resource = step, resource


def az(args):
    result = subprocess.run(['az', *args, '--only-show-errors', '--output', 'json'],
                            capture_output=True, text=True, timeout=1800)
    if result.returncode:
        raise RuntimeError('Azure request failed; inspect credentials, quota, region and zone privately')
    return json.loads(result.stdout or 'null')


def host_flags(args, hostname, source=SOURCE):
    flags = ['--hostname', hostname, '--profile', args.profile, '--overrides', str(source / 'override.json'),
             '--data-device', '/dev/disk/azure/scsi1/lun0']
    for cidr in args.operator_cidr:
        flags.extend(['--operator-cidr', cidr])
    return flags


def source_digest(root=ROOT):
    """Hash every declared host source file with unambiguous name and length framing."""
    hasher = hashlib.sha256()
    for name in SOURCES:
        data = (root / name).read_bytes()
        hasher.update(name.encode('utf-8'))
        hasher.update(b'\0')
        hasher.update(str(len(data)).encode('ascii'))
        hasher.update(b'\0')
        hasher.update(data)
    return hasher.hexdigest()


def cloud_init(args, hostname, override, fingerprint):
    files = [{'path': str(SOURCE / name), 'permissions': '0644', 'encoding': 'gzip+b64',
              'content': base64.b64encode(gzip.compress((ROOT / name).read_bytes(), mtime=0)).decode()}
             for name in SOURCES]
    files.append({'path': str(SOURCE / 'override.json'), 'permissions': '0600',
                  'content': json.dumps(override)})
    files.append({'path': str(SOURCE / 'bootstrap-intent'), 'permissions': '0600', 'content': fingerprint})
    prepare = ['python3', str(SOURCE / HOST_SCRIPT), 'prepare', *host_flags(args, hostname),
               '--initialize-empty-disk']
    return '#cloud-config\n' + json.dumps({'package_update': True,
        'packages': ['docker.io', 'docker-compose-v2', 'nftables', 'python3'],
        'write_files': files, 'runcmd': [prepare]}, indent=2) + '\n'


def upload_host_sources(ssh, override, fingerprint):
    buffer = io.BytesIO()
    metadata = {'override.json': json.dumps(override), APPLIED_INTENT: fingerprint}
    with tarfile.open(fileobj=buffer, mode='w:gz') as archive:
        for name in HOST_FILES:
            content = metadata[name].encode() if name in metadata else (ROOT / name).read_bytes()
            entry = tarfile.TarInfo(name)
            entry.mode = 0o600 if name in metadata else 0o644
            entry.size = len(content)
            archive.addfile(entry, io.BytesIO(content))
    script = ('cd ' + shlex.quote(str(SOURCE)) + '\n'
              'stage=$(mktemp -d .update.XXXXXXXX)\n'
              'trap \'rm -rf -- "$stage"\' EXIT\n'
              'tar -xzm -f - -C "$stage"\n'
              'for file do test -f "$stage/$file"; done\n'
              'trap - EXIT\n'
              'printf "%s\\n" "$stage"')
    result = subprocess.run([*ssh, shlex.join(['sudo', 'sh', '-eu', '-c', script, 'sh', *HOST_FILES])],
                            input=buffer.getvalue(), capture_output=True, timeout=60)
    stage = result.stdout.strip()
    if result.returncode or not re.fullmatch(rb'\.update\.[A-Za-z0-9]{8}', stage):
        raise RuntimeError('Host declaration upload failed')
    return SOURCE / stage.decode('ascii')


def applied_intent_check(fingerprint, bootstrap):
    marker = shlex.quote(str(SOURCE / APPLIED_INTENT))
    script = f'test "$(cat {marker})" = {shlex.quote(fingerprint)}'
    if fingerprint == bootstrap:
        return f'if test -e {marker}; then {script}; fi'
    return script


def host_apply_command(args, hostname, stage, fingerprint, bootstrap):
    source = stage or SOURCE
    command = ['python3', str(source / HOST_SCRIPT), 'apply', '--key-stdin',
               '--startup-timeout', str(args.startup_timeout)]
    if stage:
        command.extend(host_flags(args, hostname, stage))
        # Fence normal applies if this rollout fails after changing host files.
        # Publish the verified intent last, after its inputs and observer.
        script = ('cd ' + shlex.quote(str(SOURCE)) + '\n'
                  'stage=$1\nshift\n'
                  'trap \'rm -rf -- "$stage"\' EXIT\n'
                  + shlex.join(['install', '-m', '0600', '/dev/null', APPLIED_INTENT]) + '\n'
                  + shlex.join(command) + '\n'
                  'for file do mv -T -- "$stage/$file" "$file"; done')
        command = ['sh', '-eu', '-c', script, 'sh', str(stage), *HOST_FILES]
    else:
        script = applied_intent_check(fingerprint, bootstrap) + '\nexec ' + shlex.join(command)
        command = ['sh', '-eu', '-c', script]
    return shlex.join(['sudo', 'flock', '-w', '60', str(SOURCE / '.apply.lock'), *command])


def provision(args):
    context = ProvisioningContext()
    try:
        return _provision(args, context)
    except Exception as error:
        # Exception messages, commands and provider output can contain secrets.
        # Context is supplied explicitly; names are used only after validation.
        raise ProvisioningError(f'Provisioning refused or incomplete: step={context.step}; '
                                f'resource={context.resource}; exception={type(error).__name__}. '
                                'Inspect private inputs, resource drift and Azure capacity.') from None


def _provision(args, context):
    # Validate all local inputs and credentials before any provider write.
    for value in (args.name, args.resource_group, args.location, args.admin_user,
                  args.disk_name, args.nsg_name, args.public_ip_name, args.vnet_name, args.nic_name, args.subnet_name):
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', value):
            raise ValueError('invalid Azure name')
    if not re.fullmatch(r'[a-z][a-z0-9-]{1,61}[a-z0-9]', args.dns_label):
        raise ValueError('invalid Azure DNS label')
    args.operator_cidr = sorted({str(ipaddress.ip_network(cidr, strict=True)) for cidr in args.operator_cidr})
    network = ipaddress.ip_network(args.vnet_cidr, strict=True)
    subnet = ipaddress.ip_network(args.subnet_cidr, strict=True)
    if network.version != 4 or subnet.version != 4 or not subnet.subnet_of(network):
        raise ValueError('subnet must be inside the IPv4 virtual network')
    if not args.operator_cidr or any(ipaddress.ip_network(cidr).prefixlen == 0 for cidr in args.operator_cidr):
        raise ValueError('restricted SSH operator CIDRs are required')
    if args.disk_size < 1 or not 3000 <= args.disk_iops <= 80000 or not 125 <= args.disk_mbps <= 1200:
        raise ValueError('invalid Premium SSD v2 size or performance')
    if args.disk_iops > max(3000, args.disk_size * 500) or args.disk_mbps > args.disk_iops // 4:
        raise ValueError('Premium SSD v2 performance exceeds disk size/IOPS limits')
    public_key = args.ssh_public_key.read_text().strip()
    if not public_key.startswith(('ssh-ed25519 ', 'ssh-rsa ', 'ecdsa-sha2-')):
        raise ValueError('use an SSH public key file')
    if args.ssh_identity.is_symlink() or not args.ssh_identity.is_file() or args.ssh_identity.stat().st_mode & 0o077:
        raise ValueError('SSH identity file must be private and regular')
    if args.ssh_identity.resolve().is_relative_to(ROOT):
        raise ValueError('SSH identity file must stay outside the repository')
    secret = host.key(args)
    image_parts = args.image.split(':')
    if not args.image.startswith('/subscriptions/') and (len(image_parts) != 4 or not all(image_parts)):
        raise ValueError('image requires a full Ubuntu 24.04 image URN or image resource ID')
    override = json.loads(args.overrides.read_text()) if args.overrides else {'services': {}}
    services = host.infra.resolve(args.profile, args.overrides)
    # Ask Azure which capabilities the selected VM actually has. Do not assume
    # every region/zone offers the example SKU or sufficient disk throughput.
    context.at('inspect VM capabilities', args.name)
    skus = az(['vm', 'list-skus', '--location', args.location, '--size', args.vm_size, '--all'])
    context.at('validate VM capabilities', args.name)
    matches = [sku for sku in skus if sku['name'] == args.vm_size and sku['resourceType'] == 'virtualMachines']
    if len(matches) != 1:
        raise ValueError('selected VM size is unavailable')
    sku = matches[0]
    locations = sku.get('locationInfo', [])
    if not any(args.zone in item.get('zones', []) for item in locations):
        raise ValueError('selected VM size is unavailable in the requested zone')
    if any(restriction.get('type') == 'Location' or args.zone in restriction.get('restrictionInfo', {}).get('zones', [])
           for restriction in sku.get('restrictions', [])):
        raise ValueError('selected VM size is restricted in this location/zone')
    capabilities = {item['name']: item['value'] for item in sku['capabilities']}
    memory = float(capabilities['MemoryGB']) * 1024**3
    desired = services['weaviate']['deploy']['resources']['limits']
    if int(desired['memory']) + 2 * 1024**3 > memory:
        raise ValueError('selected VM RAM cannot fit declared container memory plus 2 GiB headroom')
    if int(capabilities['UncachedDiskIOPS']) < args.disk_iops or int(capabilities['UncachedDiskBytesPerSecond']) < args.disk_mbps * 1000000:
        raise ValueError('selected VM uncached disk limits are below requested disk performance')
    cpus = str(min(float(desired['cpus']), float(capabilities['vCPUs'])))
    override = host.infra.merge(override, {'services': {'weaviate': {'deploy': {'resources': {'limits': {'cpus': cpus}}}}}})
    if args.disk_size * 1024**3 < int(services['weaviate'].get('x-quivr-storage-budget-bytes', '0')):
        raise ValueError('disk size is below the declared profile storage budget')
    # Names and content fingerprint bind resources to this declaration. A changed
    # declaration needs an explicit migration, never an implicit replacement.
    context.at('resolve resource declaration', args.name)
    intent = {name: getattr(args, name) for name in ('name', 'location', 'zone', 'vm_size', 'image', 'admin_user',
              'dns_label', 'disk_name', 'nsg_name', 'public_ip_name', 'vnet_name', 'nic_name', 'subnet_name',
              'vnet_cidr', 'subnet_cidr',
              'disk_size', 'disk_iops', 'disk_mbps', 'operator_cidr')}
    intent['public_key'] = public_key
    intent['override'] = override
    intent['profile'] = args.profile
    intent['source'] = source_digest(ROOT)
    fingerprint = hashlib.sha256(json.dumps(intent, sort_keys=True).encode()).hexdigest()
    group_flags = ['--resource-group', args.resource_group]
    context.at('inspect resource group', args.resource_group)
    if not az(['group', 'exists', '--name', args.resource_group]):
        if args.reconcile:
            raise ValueError('host reconciliation requires an existing resource group')
        context.at('create resource group', args.resource_group)
        az(['group', 'create', '--name', args.resource_group, '--location', args.location])
    # Enumerate instead of treating permission/transport errors as "not found".
    context.at('list resources', args.resource_group)
    resources = az(['resource', 'list', *group_flags])
    vm_kind = 'Microsoft.Compute/virtualMachines'
    bootstrap = fingerprint
    vm_tags = [resource.get('tags') or {} for resource in resources
               if resource['type'].lower() == vm_kind.lower() and resource['name'] == args.name]
    applied = vm_tags[0].get(APPLIED_TAG) if len(vm_tags) == 1 else None
    existing_only = args.reconcile or applied is not None
    if existing_only:
        # Keep the cloud ownership identity distinct from the installed release.
        context.at('identify existing bootstrap intent', args.name)
        bootstrap = vm_tags[0].get('quivr-vector-intent') if len(vm_tags) == 1 else None
        if not isinstance(bootstrap, str) or not re.fullmatch(r'[0-9a-f]{64}', bootstrap):
            raise ValueError('host reconciliation requires an owned, bootstrapped VM')
        if not args.reconcile and applied != fingerprint:
            raise ValueError('installed host intent differs from the declaration; use --reconcile')
    tags = {'quivr-vector-host': args.name, 'quivr-vector-intent': bootstrap}
    tag_args = [f'{name}={value}' for name, value in tags.items()]

    def existing(kind, name):
        context.at('validate resource ownership', name)
        found = [resource for resource in resources if resource['type'].lower() == kind.lower() and resource['name'] == name]
        if len(found) > 1 or (found and any(found[0].get('tags', {}).get(key) != value for key, value in tags.items())):
            raise ValueError('resource ownership or declaration drift; no replacement will be made')
        if existing_only and not found:
            raise ValueError('host reconciliation requires all declared resources')
        return found[0] if found else None
    # Check every selected existing resource before creating anything else.
    kinds = {'vm': (vm_kind, args.name),
             'disk': ('Microsoft.Compute/disks', args.disk_name),
             'nsg': ('Microsoft.Network/networkSecurityGroups', args.nsg_name),
             'ip': ('Microsoft.Network/publicIPAddresses', args.public_ip_name),
             'vnet': ('Microsoft.Network/virtualNetworks', args.vnet_name),
             'nic': ('Microsoft.Network/networkInterfaces', args.nic_name)}
    present = {role: existing(*value) for role, value in kinds.items()}
    if not present['nsg']:
        context.at('create network security group', args.nsg_name)
        az(['network', 'nsg', 'create', *group_flags, '--name', args.nsg_name,
            '--location', args.location, '--tags', *tag_args])
    context.at('inspect network security rules', args.nsg_name)
    rules = az(['network', 'nsg', 'rule', 'list', *group_flags, '--nsg-name', args.nsg_name])
    context.at('validate network security rules', args.nsg_name)
    expected_rules = [('https', '100', 'Allow', 'Tcp', '443', ['*']),
                      ('operator-ssh', '110', 'Allow', 'Tcp', '22', args.operator_cidr),
                      ('deny-inbound', '120', 'Deny', '*', '*', ['*'])]
    if {rule['name'] for rule in rules} - {rule[0] for rule in expected_rules}:
        raise ValueError('unexpected NSG rules; inspect them before provisioning')
    for name, priority, access, protocol, port, sources in expected_rules:
        context.at('validate network security rule ' + name, args.nsg_name)
        found = [rule for rule in rules if rule['name'] == name]
        if found:
            rule = found[0]
            source = rule.get('sourceAddressPrefixes') or [rule.get('sourceAddressPrefix')]
            if (str(rule['priority']), rule['access'], rule['protocol'].lower(), rule['destinationPortRange'],
                sorted(source), rule['direction']) != (priority, access, protocol.lower(), port, sorted(sources), 'Inbound'):
                raise ValueError('NSG rule drift; refusing to alter existing network rules')
        else:
            if existing_only:
                raise ValueError('host reconciliation requires the declared network rules')
            context.at('create network security rule ' + name, args.nsg_name)
            az(['network', 'nsg', 'rule', 'create', *group_flags, '--nsg-name', args.nsg_name, '--name', name,
                '--priority', priority, '--access', access, '--protocol', protocol, '--direction', 'Inbound',
                '--source-address-prefixes', *sources, '--source-port-ranges', '*',
                '--destination-address-prefixes', '*', '--destination-port-ranges', port])
    if not present['vnet']:
        context.at('create virtual network', args.vnet_name)
        az(['network', 'vnet', 'create', *group_flags, '--name', args.vnet_name, '--location', args.location,
            '--address-prefixes', args.vnet_cidr, '--subnet-name', args.subnet_name, '--subnet-prefixes', args.subnet_cidr,
            '--tags', *tag_args])
    context.at('inspect virtual network', args.vnet_name)
    network_state = az(['network', 'vnet', 'show', *group_flags, '--name', args.vnet_name])
    context.at('validate virtual network', args.vnet_name)
    subnets = [subnet for subnet in network_state.get('subnets', []) if subnet['name'] == args.subnet_name]
    if (network_state.get('addressSpace', {}).get('addressPrefixes') != [args.vnet_cidr] or
            len(subnets) != 1 or subnets[0].get('addressPrefix') != args.subnet_cidr):
        raise ValueError('virtual network or subnet address drift')
    if not present['ip']:
        context.at('create public IP', args.public_ip_name)
        az(['network', 'public-ip', 'create', *group_flags, '--name', args.public_ip_name, '--location', args.location,
            '--sku', 'Standard', '--allocation-method', 'Static', '--zone', args.zone,
            '--dns-name', args.dns_label, '--tags', *tag_args])
    context.at('inspect public IP', args.public_ip_name)
    public_ip = az(['network', 'public-ip', 'show', *group_flags, '--name', args.public_ip_name])
    context.at('validate public IP', args.public_ip_name)
    if (public_ip.get('zones') != [args.zone] or public_ip['dnsSettings']['domainNameLabel'] != args.dns_label or
            public_ip.get('sku', {}).get('name') != 'Standard' or
            public_ip.get('publicIPAllocationMethod') != 'Static' or public_ip.get('publicIPAddressVersion') != 'IPv4'):
        raise ValueError('public IP SKU, allocation, address family, zone or DNS label drift')
    hostname = public_ip['dnsSettings']['fqdn']
    if not present['nic']:
        context.at('create network interface', args.nic_name)
        az(['network', 'nic', 'create', *group_flags, '--name', args.nic_name, '--location', args.location,
            '--vnet-name', args.vnet_name, '--subnet', args.subnet_name, '--network-security-group', args.nsg_name,
            '--public-ip-address', args.public_ip_name, '--accelerated-networking', 'true', '--tags', *tag_args])
    context.at('inspect network interface', args.nic_name)
    nic = az(['network', 'nic', 'show', *group_flags, '--name', args.nic_name])
    context.at('validate network interface', args.nic_name)
    prefix, separator, _ = public_ip['id'].partition('/providers/')
    if not separator:
        raise ValueError('cannot establish Azure resource identity')
    def identifier(kind, name):
        return (prefix + '/providers/' + kind + '/' + name).lower()
    configurations = nic.get('ipConfigurations', [])
    if (len(configurations) != 1 or not nic.get('enableAcceleratedNetworking') or
            (nic.get('networkSecurityGroup') or {}).get('id', '').lower() != identifier(*kinds['nsg']) or
            (configurations[0].get('publicIPAddress') or {}).get('id', '').lower() != public_ip['id'].lower() or
            (configurations[0].get('subnet') or {}).get('id', '').lower() !=
            identifier('Microsoft.Network/virtualNetworks', args.vnet_name) + '/subnets/' + args.subnet_name.lower()):
        raise ValueError('NIC network, public IP or security group drift; refusing private credential transport')
    if (nic.get('virtualMachine') or {}).get('id') and (not present['vm'] or
            nic['virtualMachine']['id'].lower() != present['vm']['id'].lower()):
        raise ValueError('NIC belongs to another VM')
    if not present['disk']:
        context.at('create data disk', args.disk_name)
        az(['disk', 'create', *group_flags, '--name', args.disk_name, '--location', args.location,
            '--zone', args.zone, '--sku', 'PremiumV2_LRS', '--size-gb', str(args.disk_size),
            '--disk-iops-read-write', str(args.disk_iops), '--disk-mbps-read-write', str(args.disk_mbps),
            '--tags', *tag_args])
    context.at('inspect data disk', args.disk_name)
    disk = az(['disk', 'show', *group_flags, '--name', args.disk_name])
    context.at('validate data disk', args.disk_name)
    if (disk['sku']['name'], disk.get('zones'), disk['diskSizeGB'], disk['diskIOPSReadWrite'], disk['diskMBpsReadWrite']) != (
            'PremiumV2_LRS', [args.zone], args.disk_size, args.disk_iops, args.disk_mbps):
        raise ValueError('data disk performance, size or zone drift')
    if disk.get('managedBy') and not present['vm']:
        raise ValueError('data disk is already attached to another VM')
    if not present['vm']:
        context.at('prepare VM bootstrap', args.name)
        with tempfile.TemporaryDirectory() as directory:
            init = Path(directory) / 'cloud-init.json'
            init.write_text(cloud_init(args, hostname, override, fingerprint))
            context.at('create VM', args.name)
            az(['vm', 'create', *group_flags, '--name', args.name, '--location', args.location, '--zone', args.zone,
                '--size', args.vm_size, '--image', args.image, '--admin-username', args.admin_user,
                '--ssh-key-values', public_key, '--nics', args.nic_name, '--attach-data-disks', disk['id'],
                '--data-disk-caching', 'None', '--data-disk-delete-option', 'Detach',
                '--disk-controller-type', 'SCSI',
                '--custom-data', str(init), '--tags', *tag_args])
    context.at('inspect VM', args.name)
    vm = az(['vm', 'show', *group_flags, '--name', args.name])
    context.at('validate VM', args.name)
    if vm.get('zones') != [args.zone] or vm['hardwareProfile']['vmSize'] != args.vm_size:
        raise ValueError('VM size or zone drift')
    reference = vm['storageProfile'].get('imageReference', {})
    if args.image.startswith('/subscriptions/'):
        image_matches = reference.get('id', '').lower() == args.image.lower()
    else:
        image_matches = all(reference.get(field, '').lower() == value.lower()
                            for field, value in zip(('publisher', 'offer', 'sku'), image_parts[:3]))
        image_matches = image_matches and (image_parts[3] == 'latest' or
            image_parts[3] in (reference.get('version'), reference.get('exactVersion')))
    network_interfaces = vm.get('networkProfile', {}).get('networkInterfaces', [])
    if (not image_matches or vm.get('osProfile', {}).get('adminUsername') != args.admin_user or
            len(network_interfaces) != 1 or network_interfaces[0]['id'].lower() != nic['id'].lower()):
        raise ValueError('VM image, admin user or network attachment drift')
    context.at('validate data disk attachment', args.disk_name)
    attachments = vm['storageProfile']['dataDisks']
    if len(attachments) != 1 or attachments[0]['managedDisk']['id'].lower() != disk['id'].lower() or (
            attachments[0]['lun'], attachments[0]['caching'], attachments[0].get('deleteOption')) != (0, 'None', 'Detach'):
        raise ValueError('VM data disk mapping, caching or retention drift')
    if disk.get('managedBy') and disk['managedBy'].lower() != vm['id'].lower():
        raise ValueError('disk belongs to another VM')
    # Authenticate the very first SSH connection through Azure's control plane,
    # before sending credentials. Retain this public host key outside the repo;
    # an unchanged rerun needs no RunCommand invocation or trust change.
    context.at('validate SSH trust file', args.name)
    trust = args.known_hosts or args.ssh_identity.parent / (args.name + '.vector-host.known_hosts')
    if trust.is_symlink() or (trust.exists() and (not trust.is_file() or trust.stat().st_mode & 0o077)):
        raise ValueError('host trust file must be private and regular')
    if trust.resolve().is_relative_to(ROOT):
        raise ValueError('host trust file must stay outside the repository')
    if not trust.exists():
        context.at('retrieve SSH host key', args.name)
        response = az(['vm', 'run-command', 'invoke', *group_flags, '--name', args.name,
                       '--command-id', 'RunShellScript', '--scripts', 'cat /etc/ssh/ssh_host_ed25519_key.pub'])
        context.at('validate SSH host key', args.name)
        output = '\n'.join(item.get('message', '') for item in response.get('value', []))
        keys = re.findall(r'^ssh-ed25519 ([A-Za-z0-9+/=]+)(?: .*)?$', output, re.MULTILINE)
        if len(keys) != 1:
            raise ValueError('Azure did not provide one verifiable SSH host key')
        context.at('save SSH host key', args.name)
        host.write(trust, hostname + ' ssh-ed25519 ' + keys[0] + '\n')
    context.at('verify host bootstrap', args.name)
    ssh = ['ssh', '-i', str(args.ssh_identity), '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
           '-o', 'UserKnownHostsFile=' + str(trust),
           '-o', 'ConnectTimeout=10', '-o', 'ConnectionAttempts=10', args.admin_user + '@' + hostname]
    verify = ('sudo cloud-init status --wait >/dev/null && test "$(sudo cat '
              + shlex.quote(str(SOURCE / 'bootstrap-intent')) + ')" = ' + shlex.quote(bootstrap))
    if not args.reconcile:
        verify += ' && ' + shlex.join(['sudo', 'sh', '-eu', '-c', applied_intent_check(fingerprint, bootstrap)])
    verified = subprocess.run([*ssh, verify],
        capture_output=True, text=True, timeout=1800)
    if verified.returncode:
        raise RuntimeError('Host trust or bootstrap verification failed; no credential sent')
    stage = None
    if args.reconcile:
        context.at('upload host declaration', args.name)
        stage = upload_host_sources(ssh, override, fingerprint)
    context.at('apply host configuration', args.name)
    result = subprocess.run([*ssh, host_apply_command(args, hostname, stage, fingerprint, bootstrap)],
                            input=secret, capture_output=True, text=True, timeout=1800 + args.startup_timeout)
    if result.returncode:
        try:
            status = json.loads(result.stdout)['check']['status']
        except (KeyError, TypeError, ValueError):
            status = None
        if status in ('drift', 'unknown'):
            context.at(f'verify running Weaviate ({status})', args.name)
        raise RuntimeError('Host initialization failed; inspect cloud-init and host services privately, then rerun')
    status = json.loads(result.stdout)['status']
    if args.reconcile and applied != fingerprint:
        context.at('record verified host intent', args.name)
        az(['tag', 'update', '--operation', 'Merge', '--resource-id', vm['id'],
            '--tags', f'{APPLIED_TAG}={fingerprint}'])
    return {'status': status, 'weaviate_url': 'https://' + hostname,
            'weaviate_grpc_address': hostname + ':443',
            'resolved_weaviate_limits': {**desired, 'cpus': cpus},
            'host_check': 'Run host.py check on the VM; validate protected requests and engine searches.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--resource-group', required=True)
    parser.add_argument('--name', required=True)
    parser.add_argument('--location', required=True)
    parser.add_argument('--zone', required=True, choices=['1', '2', '3'])
    parser.add_argument('--dns-label', required=True)
    parser.add_argument('--admin-user', required=True)
    parser.add_argument('--operator-cidr', action='append', required=True)
    parser.add_argument('--ssh-public-key', type=Path, required=True)
    parser.add_argument('--ssh-identity', type=Path, required=True)
    parser.add_argument('--known-hosts', type=Path, help='private Azure-verified trust file; defaults beside SSH identity')
    parser.add_argument('--reconcile', action='store_true', help='apply current host settings to an existing owned VM')
    parser.add_argument('--startup-timeout', type=int, default=600, metavar='SECONDS',
                        help='maximum wait for Weaviate startup after apply (default: 600)')
    parser.add_argument('--vm-size', default='Standard_E8s_v5')
    parser.add_argument('--image', default='Canonical:ubuntu-24_04-lts:server:latest')
    parser.add_argument('--disk-size', type=int, default=512, help='GiB')
    parser.add_argument('--disk-iops', type=int, default=12000)
    parser.add_argument('--disk-mbps', type=int, default=250, help='MB/s; leaves headroom under E8s_v5 290 MB/s cap')
    parser.add_argument('--vnet-cidr', default='10.80.0.0/16')
    parser.add_argument('--subnet-cidr', default='10.80.0.0/24')
    for name in ('disk', 'nsg', 'public-ip', 'vnet', 'nic', 'subnet'):
        parser.add_argument('--' + name + '-name')
    parser.add_argument('--profile', default='large')
    parser.add_argument('--overrides', type=Path)
    parser.add_argument('--api-key-file', type=Path)
    parser.set_defaults(key_stdin=False)
    args = parser.parse_args()
    if args.startup_timeout < 0:
        parser.error('startup timeout must be nonnegative')
    for name in ('disk', 'nsg', 'public_ip', 'vnet', 'nic', 'subnet'):
        if getattr(args, name + '_name') is None:
            setattr(args, name + '_name', args.name + '-' + name.replace('_', '-'))
    try:
        print(json.dumps(provision(args), indent=2))
        return 0
    except ProvisioningError as error:
        print(str(error), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
