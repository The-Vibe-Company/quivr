"""The storage snapshot must use the deployment's verified connection policy."""
import unittest
from unittest.mock import patch

import measure_storage


class StorageConnectionPolicy(unittest.TestCase):
    def test_literal_tls_configuration_and_native_modes(self):
        base = {'database_url': 'postgresql://reader:password@db.example/store'}
        for mode in (None, 'prefer', 'allow', 'require', 'verify-ca'):
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                measure_storage.connection_environment({
                    'database_url': base['database_url'] + (f'?sslmode={mode}' if mode else '')})
        for mode in ('disable', 'verify-full'):
            configured = measure_storage.connection_environment({'database_url': base['database_url'] + f'?sslmode={mode}'})
            self.assertEqual(configured['PGSSLMODE'], mode)
        policy = {**base, 'tls': {'postgres': {'enabled': True, 'ca_file': '/certs/ca.pem',
                  'cert_file': '/certs/client.pem', 'key_file': '/certs/client.key',
                  'server_name': 'verified.example'}}}
        with patch('measure_storage.socket.getaddrinfo', return_value=[(None, None, None, None, ('192.0.2.1', 5432))]):
            configured = measure_storage.connection_environment(policy)
        self.assertEqual({key: configured[key] for key in ('PGSSLMODE', 'PGSSLROOTCERT', 'PGSSLCERT',
                         'PGSSLKEY', 'PGHOST', 'PGHOSTADDR', 'PGSSLMINPROTOCOLVERSION')}, {
            'PGSSLMODE': 'verify-full', 'PGSSLROOTCERT': '/certs/ca.pem', 'PGSSLCERT': '/certs/client.pem',
            'PGSSLKEY': '/certs/client.key', 'PGHOST': 'verified.example', 'PGHOSTADDR': '192.0.2.1',
            'PGSSLMINPROTOCOLVERSION': 'TLSv1.2'})
        for tls in ({'enabled': False, 'ca_file': '/certs/ca.pem'}, {'cert_file': '/certs/client.pem'},
                    {'enabled': 'true'}):
            with self.subTest(tls=tls), self.assertRaises(ValueError):
                measure_storage.connection_environment({**base, 'tls': {'postgres': tls}})
        self.assertEqual(measure_storage.connection_environment({**base, 'tls': {'postgres': {'enabled': False}}})['PGSSLMODE'], 'disable')


if __name__ == '__main__':
    unittest.main()
