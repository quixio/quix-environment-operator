from pathlib import Path
import os
import re
import subprocess
import tempfile
import unittest


class ApplicationWorkflow(unittest.TestCase):
    def setUp(self):
        workflow = (Path(__file__).resolve().parents[1] / '.github/workflows/build.yml').read_text()
        # Job boundaries in this workflow are two-space-indented mapping keys.
        self.jobs = dict(re.findall(r'^  ([\w-]+):\n(.*?)(?=^  [\w-]+:|\Z)',
                                    workflow.split('jobs:\n', 1)[1], re.M | re.S))

    def test_pr_build_and_tests_are_independent_of_publication(self):
        job = self.jobs['validate-pr']
        self.assertIn("    if: ${{ github.event_name == 'pull_request' }}\n", job)
        self.assertNotRegex(job, r'^    needs:', 'PR tests must not depend on skipped publish jobs')
        self.assertIn('    permissions:\n      contents: read\n', job)
        self.assertIn('          persist-credentials: false\n', job)
        self.assertNotRegex(job, r'secrets\.|: write|login-action|docker-push|docker push|git push|chart-releaser|continue-on-error:')
        self.assertEqual(re.findall(r'^      - run: (.+)$', job, re.M),
                         ['make docker-build', 'make docker-test'])
        self.assertNotRegex(job, r'^        if:', 'Neither validation step may be skipped')

    def test_non_pr_publication_keeps_main_guard_and_dependencies(self):
        self.assertIn("    if: ${{ github.ref == 'refs/heads/main' && github.event_name != 'pull_request' }}\n",
                      self.jobs['extract-versions'])
        for name in ('build-test-push-docker', 'build-and-push-helm'):
            with self.subTest(job=name):
                job = self.jobs[name]
                self.assertIn('    needs: extract-versions\n', job)
                self.assertNotRegex(job, r'^    if:', 'Do not override skipped dependency handling')
        docker = self.jobs['build-test-push-docker']
        self.assertLess(docker.index('make docker-build'), docker.index('make docker-test'))
        self.assertLess(docker.index('make docker-test'), docker.index('docker/login-action'))
        self.assertLess(docker.index('docker/login-action'), docker.index('make docker-push'))

    def test_pr_commands_propagate_build_and_test_failures(self):
        commands = re.findall(r'^      - run: (.+)$', self.jobs['validate-pr'], re.M)
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            make = folder / 'make'
            make.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "$CALL_LOG"\n'
                            '[ "$1" != "$FAIL_TARGET" ]\n')
            make.chmod(0o755)
            for failure in ('', 'docker-build', 'docker-test'):
                with self.subTest(failure=failure):
                    log = folder / 'calls'
                    log.unlink(missing_ok=True)
                    env = dict(os.environ, PATH=directory + os.pathsep + os.environ['PATH'],
                               CALL_LOG=str(log), FAIL_TARGET=failure)
                    # GitHub's default bash steps stop the job on the first failed command.
                    result = subprocess.run(['bash', '-e', '-c', '\n'.join(commands)], env=env,
                                            cwd=directory, capture_output=True)
                    self.assertEqual(result.returncode == 0, not failure)
                    self.assertEqual(log.read_text().splitlines(), ['docker-build'] if failure == 'docker-build'
                                     else ['docker-build', 'docker-test'])


if __name__ == '__main__':
    unittest.main()
